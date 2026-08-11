# ADR-0055: ワンセグは同じ予約の二本目のサービスとして保存する

- Status: Accepted
- Date: 2026-08-11
- Deciders: Project owner（v1.0までの技術判断と必要なDB変更をCodexへ明示委任）
- Delegated reviewer: Codex
- Related: ADR-0023、ADR-0031、ADR-0032、ADR-0039、ADR-0041、Plan 0070、Handoff 0045
- Product copy path: `docs/adr/0055-oneseg-separate-output.md`
- Product sync state: NOT COPIED
- Supersedes: 下記「既存Accepted文書との差分」に示すワンセグ対象外の部分だけ
- Superseded by: None

## 背景

KonomiTV v0.14.1は、録画設定の`is_oneseg_separate_output_enabled=true`をCtrlCmdの
`partial_rec_flag=1`へ変換する。`recording_folders`のうち
`is_oneseg_separate_recording_folder=true`の項目は`partial_rec_folder`へ送る。同梱画面では
非表示だが、予約と自動予約条件のHTTP schema、追加、変更、一覧で使われる。

Sazanamiは自動予約条件としてワンセグ設定を保存できるが、この値を含む規則から予約を作らない。
単発予約も対象外として拒否する。予約は一つのprovider locatorと録画先だけを持ち、録画executor、履歴、
復旧は`recording_segments.ordinal=0`だけを扱う。

Mirakurunのサービスストリームは指定SIDへ絞られる。メインのstreamから別SIDのワンセグを後で分離する
前提にはできない。Sazanamiの静的チャンネル設定には、network ID、TSID、SID、
`partial_reception`、provider locatorがあるため、同じ放送波のワンセグサービスを一意に選べる。

Mirakurun 4.1.3は、同じchannelへの二つ目のstreamを使用中のtunerへ先に合流させる。これは固定版の
source事実であり、互換API全体の保証ではない。未知の互換versionを文字列だけで拒否せず、実環境証拠が
ない提供元の共有動作は`PROVISIONAL`とする。

Project ownerは、v1.0までの技術判断と必要なDB・公開API変更をCodexへ明示委任した。本判断はその委任、
固定source、製品baseの読取り結果、メイン録画を先に守る既存方針に基づいてAcceptedとする。

## 判断

### ワンセグは同じ予約の任意出力にする

予約要求は、ワンセグ無効または一つのワンセグ録画先を持てる。一意に解決した後の予約は、接続先と
録画先をまとめた任意の`OneSegOutput`を保持する。規則や静的チャンネル設定を後から変更・削除しても、
作成済み予約の接続先と録画先を変えない。

対応サービスは、メインと同じnetwork ID、TSIDを持ち、SIDが異なり、
`partial_reception=true`である検証済みserviceを選ぶ。候補が一件だけの場合に限り成功する。0件、
複数件、未検証service、非canonical locatorでは、対応関係を推測しない。

単発予約と自動予約へ同じ条件を使う。対応サービスを一意に解決できない場合、または録画先の安全条件を
満たさない場合は、単発予約では要求全体を失敗させ、自動予約では規則を保存したまま利用不能理由と件数を出し、
予約を作らない。

### schema第13版で予約snapshotを保存する

事前backup付きmigrationで`reservation_oneseg_outputs`を追加する。予約IDを主keyとし、provider locator、
相対フォルダー、file名templateを保存する。行なしはワンセグ無効、行ありは有効を表す。予約の作成・
変更と同じtransactionでinsert、update、deleteする。

自動予約規則だけを実行時に参照する案は採用しない。規則削除時に対応表が消え、作成済み予約の効果が
変わるためである。既存columnへJSONを埋め込んだり、`tuning_target`へ複数の意味を持たせたりしない。

### 出力はメイン0番とワンセグ1番に固定する

録画処理のclaimは、メインのordinal 0と、ワンセグ有効時のordinal 1を一transactionで作る。2番以後を
認めず、汎用的な複数出力frameworkへ広げない。

ワンセグ用録画先は0件または1件だけ受ける。0件ならメインの録画先とtemplateを使う。1件なら
ADR-0041の相対path、plugin、template、macroの検証を再利用する。録画先とtemplateがともに空の一件は
0件へ正規化する。

完成名は、既存の展開結果にある`.ts`の直前へ`.oneseg`を加える。`program.ts`は
`program.oneseg.ts`、既定名は`<attempt-id>.oneseg.ts`になる。部分名は完成名へ`.partial`を加える。
これはSazanamiの安定した公開規則であり、未確認のEDCB命名を再現したとは表明しない。

録画試行のbyte数、CtrlCmd録画履歴、HTTP再生は従来どおり0番のメインだけを表す。1番は自身のbyte数、
file状態、integrity理由をDBへ持つ。既存の録画済み番組を二件へ増やさない。

### メインを先に開始し、失敗を分離する

メインの部分fileとprovider接続を先に確保し、録画開始をDBへ保存した後だけ、ワンセグ用の一つの
補助goroutineを始める。補助処理は独立予約や二件目のscheduler slotとして扱わず、メインをcancel、
preempt、retuneしない。

一予約が開く録画用service streamは最大二本である。同時録画数`N`に対するHTTP接続上限を最大`2N`とし、
起動時に算術overflowを拒否する。新しいtuner台数上限は加えない。各streamは既存の接続期限、読出し
停滞期限、固定回数の再接続、188 KiBのchunk上限を使う。

ワンセグだけの接続、読出し、書込み、同期、公開が失敗しても、メインが完成すれば録画試行は成功とする。
1番segmentへ部分、欠落、不一致と固定理由を残す。メインが失敗、停止、cancelになった場合は補助処理も
止め、二つのlease、file、goroutineを閉じてから終了する。

正常終了と利用者停止ではメインを先に公開する。ワンセグは188 byte以上を保存し、同期と整合確認を
終えた場合だけ公開する。録画後scriptと電源動作は補助処理の終了後に一度だけ行い、入力は従来どおり
メインfileとメインの終了状態にする。

### 復旧は二segmentを同じ録画試行として照合する

録画試行をterminalへ進める前に、0番と1番のfile状態をDBへ保存する。再起動時はordinal順に読み、
通常file、所有者、byte数、部分名、完成名を個別に確認する。メインを先に確定し、ワンセグは同期済みの
完全fileだけを公開する。判断できないfileへappendせず、同名fileを上書きしない。

ワンセグの復旧失敗だけで、整合するメイン完成fileを欠落へ戻さない。DB更新が失敗して正本を維持できない
場合は、既存方針どおり新しい録画を開始せず安全に停止する。

## 既存Accepted文書との差分

既存文書を削除または黙って書き換えず、次のワンセグ対象外だけを本ADRと
`spec/recording/oneseg-separate-output-v1.md`で置き換える。他の判断と上限は維持する。

| 既存文書 | 置き換える範囲 | 維持する範囲 |
|---|---|---|
| ADR-0032、`bounded-automatic-reservations-v1.md`のBAR-007 | ワンセグを含む規則から予約を作らない部分 | 規則保存、完成catalog、重複防止、評価上限 |
| ADR-0039、`basic-recording-settings-v1.md` | `partial_rec_flag=0`、`partial_rec_folder=0件`だけを受ける部分 | 有効状態、優先度、追従、余白、対象外を無視しない原則 |
| ADR-0041、`safe-reservation-output-v1.md` | ワンセグ用録画先を常に0件とする部分 | 相対path、plugin、macro、上書き防止、transaction保存 |
| ADR-0031 | 一予約一streamを前提にした資源見積り | 先着録画、録画枠、同時録画数、独立失敗、停止順 |

強制チューナー、連続file、ぴったり録画、複数の通常録画先は対象外のままである。
`partial_rec_flag=2`もKonomiTVの方針に合わせて拒否する。

## 結果

- KonomiTVの単発予約と自動予約で、ワンセグ別出力の設定と効果を一致させられる。
- 規則や静的設定の変更後も、作成済み予約の接続先と録画先を維持できる。
- ワンセグだけの失敗でメイン録画を失わず、再起動後も二fileを照合できる。
- 一予約あたりの接続、buffer、file、goroutineは一組だけ増える。
- schema第13版、二倍までの録画用HTTP接続、補助goroutine、復旧caseが増える。
- Mirakurun互換提供元が同じchannelを共有しない場合、ワンセグ接続が別tunerを消費する可能性がある。

## 採用しなかった案

### メインのサービスストリームをGoで二サービスへ分ける

Mirakurunのサービスストリームは指定SIDへ絞られる。チャンネル全体stream、PAT／PMTの再構成、二つの
出力filterが必要になり、現在の変更より大きいため採用しない。

### ワンセグを独立した予約にする

予約一覧、重複防止、録画枠、録画後処理が二件になり、KonomiTVの一つの録画設定と一致しないため
採用しない。

### ワンセグ失敗時にメインも失敗へ変える

補助出力の障害で主要な録画を失う。先に守る対象をメインへ固定するため採用しない。

### 任意個数の補助出力を先に設計する

現時点で必要なのはワンセグ一つである。role、queue、plugin、動的workerを増やさず、ordinal 1へ固定する。

### DBを変えず自動予約規則を実行時に読む

規則の変更・削除で予約の効果が変わり、再起動時の正本を失うため採用しない。

## 検証

- KonomiTVのCtrlCmd fieldを単発予約と自動予約で同じように読書きする。
- 対応サービス一件、0件、複数件、別TS、同じSID、未検証serviceを分ける。
- schema第12版から第13版のbackup、migration、再起動、restore、破損値拒否を確認する。
- 規則変更・削除、snapshot更新後も、作成済み予約の`OneSegOutput`を維持する。
- メイン先行、ワンセグ単独失敗、利用者停止、親cancel、再接続、file失敗を確認する。
- claimから公開までのcrash windowを、0番と1番の両方で回復する。
- body、lease、file、goroutineを閉じ、raceと複数同時録画で資源上限を守る。
- Mirakurun 4.1.3の実環境確認をsource確認と分け、未知providerを対応済みにしない。

## 製品同期

- Handoff: Handoff 0045
- Planning source commit: Handoff 0045で固定する
- Target product base commit: `75b416bcf310458d0526d9bdf1599dedcc4d8033`
- Product destination: `docs/adr/0055-oneseg-separate-output.md`
- Last synchronized product commit: NOT COPIED
- Known divergence: 製品側の既存Acceptedコピーはワンセグを対象外とし、本ADRと仕様v1がその部分だけを置き換える

## 見直す条件

- providerが一つの接続から複数serviceを安全に分離する能力を公開する。
- KonomiTVがワンセグだけの録画、複数のワンセグ録画先、file名の契約を変更する。
- Mirakurun互換提供元で、二本目の同一channel streamが主要録画を継続的に妨げる証拠が得られる。
- 録画済み一覧や再生APIでワンセグfileを独立項目として公開する必要が生じる。
