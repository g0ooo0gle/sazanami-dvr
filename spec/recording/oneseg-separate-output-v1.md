# ワンセグ別出力仕様 v1

- Status: Accepted
- Date: 2026-08-11
- Requirements: `OSG-001`～`OSG-022`
- Related decision: ADR-0055
- Target client: KonomiTV `v0.14.1` / `0a32188274b81c1e7bed642474b208bd2a543a6b`
- Target provider evidence: Mirakurun `4.1.3` / `5770073e9b30d523512858ca82f45386f51a08fd`
- Target product base: `75b416bcf310458d0526d9bdf1599dedcc4d8033`

## 目的

KonomiTVのワンセグ別出力を、同じ予約に属する最大二本のサービスストリームと録画fileとして保存する。
メイン録画を先に守り、ワンセグだけの失敗を分離し、再起動後もSQLiteとfileを安全に照合する。

本仕様は、既存仕様のワンセグ対象外だけを置き換える。自動予約の評価上限、録画先の安全性、先着録画、
同時録画数、再接続、録画後処理、履歴と再生の既存契約は、明記した差分以外を変更しない。

## 用語

メインは、予約番組の通常サービスを保存するordinal 0のsegmentである。ワンセグは、同じ放送波の
部分受信サービスを保存するordinal 1の任意segmentである。一つの録画試行は、メイン一つとワンセグ
最大一つを持てる。

ワンセグ用録画先は、CtrlCmdの`partial_rec_folder`から得る0件または1件の録画先を指す。
対応サービスは、静的チャンネルsnapshotでメインと対応付く一つの部分受信サービスである。

## 要件

### OSG-001: KonomiTV v0.14.1のfieldを同じ意味で扱う

`partial_rec_flag=0`はワンセグ無効、`partial_rec_flag=1`は別出力を有効にする。
`partial_rec_flag=2`と2を越える値は要求全体を対象外として失敗させる。値2をワンセグだけの録画へ
推測で広げない。

`partial_rec_folder`は0件または1件だけを実行可能にする。2件以上は、単発予約では要求全体を失敗させ、
自動予約では規則を保存したまま予約を作らない。

### OSG-002: 自動予約条件の読書きを維持する

自動予約条件は`PartialMode`と`PartialFolders`を、追加、一覧、変更、再起動後の一覧で意味を変えずに
返す。実行profile外の値も保存できるが、対応したように丸めて予約を作らない。

### OSG-003: 単発予約と自動予約へ同じprofileを使う

単発予約の追加・変更と、自動予約から作る予約は、ワンセグ有効値、録画先、service解決、保存先検証を
共有する。一方だけで値を捨てない。予約一覧は、保存済みの有効状態とワンセグ用録画先を読み戻す。

### OSG-004: 対応サービスを一意に解決する

静的チャンネルsnapshotで、次をすべて満たすserviceを探す。

- メインとnetwork ID、TSIDが同じ。
- メインとSIDが異なる。
- `partial_reception=true`、`Verified=true`、`Selected=true`である。
- provider locatorが、既存のMirakurun service locatorと同じcanonical形式である。

候補が一件の場合だけ成功する。0件、複数件、context終了、snapshot不正では、locatorを推測しない。

### OSG-005: version文字列だけで拒否しない

provider runtimeのversionはprovenanceへ残すが、未知のversion文字列だけでワンセグを拒否しない。
二つのservice locatorへ既存のstream契約で接続できることを必要能力とする。同じ物理tunerの共有は
別の実環境証拠とし、未確認の互換提供元は`PROVISIONAL`と表示する。

### OSG-006: 予約時点の効果を固定する

予約要求は、無効または一つのワンセグ録画先を持てる。一意に解決した予約は、provider locatorと
録画先をまとめた任意の`OneSegOutput`を持つ。規則、完成catalog、静的チャンネル設定を後から
変更・削除しても、作成済み予約の`OneSegOutput`を変えない。

### OSG-007: schema第13版へ任意の一行を保存する

事前backup付きの明示migrationで、次の意味を持つ`reservation_oneseg_outputs` tableを追加する。

| column | 型と上限 | 意味 |
|---|---|---|
| `reservation_id` | 16 byte BLOB、主key、`reservations(id)`を参照 | 対象予約 |
| `provider_service_locator` | UTF-8 text、1～256 byte | 予約時点で固定した接続先 |
| `output_folder` | UTF-8 text、0～256 byte | 録画root内の相対folder。空は既定またはメイン継承 |
| `output_template` | UTF-8 text、0～512 byte | 対応macroだけを含むfile名template |

行がなければワンセグ無効、行があれば有効である。NUL、不正UTF-8、絶対path、親参照、上限超過、
不正locatorを受理しない。予約を物理削除しない既存方針に合わせ、外部keyは`ON DELETE RESTRICT`とする。

### OSG-008: 予約とワンセグ設定を一transactionで変更する

予約作成は、`reservations`、CtrlCmd番号、任意の`reservation_oneseg_outputs`、自動予約対応表を一つの
transactionで保存する。予約変更は、既存の比較条件を満たした場合だけワンセグ行をinsert、update、
deleteする。途中失敗で予約本体とワンセグ設定を分けない。

### OSG-009: migrationは第12版からだけ進める

schema第12版の完全な事前backupを作り、manifestとbyte数を読み戻した後だけ第13版へ進める。第13版を
二度適用しない。第11版以前、14版以後、破損schema、backup失敗では製品serveを始めない。restore後は
第12版の状態へ戻り、再度同じmigrationを適用できる。

### OSG-010: ワンセグ用録画先は既存の安全profileを使う

ワンセグ用録画先1件は、通常録画先と同じ次の条件を使う。

- `rec_folder`は空、または録画root内のcanonicalな相対folder。
- `write_plug_in`は空または`Write_Default.dll`。
- `rec_name_plug_in`は空、`RecName_Macro.dll`、または対応templateを付けた同plugin。
- reserved fieldは空。
- templateは既存の対応macro、長さ、文字、単一file名の制約を守る。

folderとtemplateがともに空の一件は0件へ正規化する。他のplugin、大文字・小文字違い、2件以上、
truncated構造を部分的に受けない。

### OSG-011: 録画先0件はメイン設定を継承する

`partial_rec_folder`が0件なら、ワンセグfileのfolderとtemplateはメインの`OutputSettings`を使う。
1件ならその値を使い、空の項目だけをメインから補完しない。これにより指定済みのワンセグ録画先と
メイン継承を区別する。

### OSG-012: file名へ`.oneseg`を一度だけ加える

既存の安全な名前展開で得た`.ts`完成名の直前へ`.oneseg`を加える。

| 元の完成名 | ワンセグ完成名 | ワンセグ部分名 |
|---|---|---|
| `<attempt-id>.ts` | `<attempt-id>.oneseg.ts` | `<attempt-id>.oneseg.ts.partial` |
| `program.ts` | `program.oneseg.ts` | `program.oneseg.ts.partial` |

suffixは大小文字を区別せず末尾の`.ts`へ適用する。元の完成名がすでに`.oneseg.ts`で終わる場合も、
メインとの衝突を避けるため`.oneseg`をもう一つ加え、`program.oneseg.oneseg.ts`とする。一回の計画作成で
加えるsuffixは一つだけとする。完成名と部分名は同じdirectoryに置き、通常file、symlink、hard link、
既存完成名を上書きしない。

### OSG-013: claimは最大二segmentを一度に作る

録画処理のclaimは、メインのordinal 0を必ず作り、ワンセグ有効時だけordinal 1を同じtransactionで
作る。各segmentは別のID、部分path、完成path、byte数、同期・公開状態を持つ。2番以後、ordinal重複、
同じpath、既存pathとの衝突を拒否する。

### OSG-014: メインを先に開始する

executorは次の順序を守る。

1. 録画処理と最大二segmentをclaimする。
2. メインの部分fileを作る。
3. メインのservice streamを開く。
4. 録画開始と更新後の予定終了をDBへ保存する。
5. ワンセグ用の一つの補助goroutineを始め、部分fileとstreamを開く。

ワンセグ準備のためにメイン開始を待たせず、補助処理からメインをcancel、preempt、retuneしない。

### OSG-015: 資源を一予約二組へ制限する

ワンセグ有効な予約は、追加でservice stream一つ、188 KiB chunk一つ、部分file一つ、補助goroutine一つを
使える。queue、daemon、polling、無制限bufferを追加しない。

同時録画数`N`に対する録画用HTTP接続上限は最大`2N`とする。`2N`の算術overflow、0以下、不正値は
起動時に拒否する。新しいtuner台数上限は設けず、20台以上の既存警告方針を変えない。

### OSG-016: 二つのstreamは独立して期限と再接続を守る

メインとワンセグは、それぞれ既存の接続・header期限、read idle期限、予定終了、絶対上限、最大3回の
再接続を使う。古いleaseをcancelしてcloseした後だけ再接続する。同じstreamを二つのfileへ書かず、
一つのleaseを複数goroutineから読まない。

### OSG-017: ワンセグだけの失敗をメインから分離する

ワンセグの接続、HTTP状態、content type、読出し停滞、早期終了、再接続枯渇、file作成、書込み、同期、
公開が失敗しても、メインが正常なら録画試行を成功へ進める。ordinal 1へbyte数、`PARTIAL`、`MISSING`、
`MISMATCHED`の該当状態と、検索語やpathを含まない固定integrity理由を保存する。

メインが失敗した場合、補助処理をcancelして終了を待つ。ワンセグだけを完成録画として公開せず、
安全な部分fileがあれば既存の部分状態として維持する。

### OSG-018: 利用者停止は二つを止めてから公開する

録画中の利用者停止、親contextのcancel、process shutdownは二つのleaseへ伝える。補助goroutine、lease、
body、fileをすべて閉じてからDBをterminalへ進める。

利用者停止では、メインが188 byte以上なら既存どおり再生可能な完成名へ公開する。ワンセグも188 byte以上、
同期済み、整合ありの場合だけワンセグ完成名へ公開する。録画試行の状態と理由はメインの結果を使う。

### OSG-019: 完成処理はメインを先に確定する

二つのstreamを止め、部分fileを同期して閉じた後、録画試行を`FINALIZING`へ進める。メインを先に
hard link、directory sync、DB確定し、次にワンセグを処理する。ワンセグ失敗はordinal 1へ残し、
メインの`FINAL`を取り消さない。

録画試行をterminalへ進めるのは、ordinal 0と任意のordinal 1について、公開済み、部分、欠落のいずれかを
DBへ保存した後に限る。録画後scriptと電源動作はその後に一度だけ行い、メインfileとメイン結果を使う。

### OSG-020: 再起動時に二segmentを順番に照合する

復旧は録画試行ごとにsegmentをordinal順で最大二件読む。各file planについて、所有者、通常file、
symlink、部分・完成fileの同一性、byte数、同期・公開状態を個別に確認する。

- ordinal 0を先に復旧する。
- ordinal 1は同期済みでbyte数が一致する場合だけ完成名へ公開する。
- 完成名がすでに別fileなら上書きせず`MISMATCHED`にする。
- 不明な部分fileへappendしない。
- ワンセグだけの不一致で、整合するメイン完成fileを欠落へ戻さない。
- DB正本を更新できない場合は、新しい録画を開始せずprocessを安全に止める。

### OSG-021: 既存の履歴と再生はメインを表す

`recording_attempts.byte_count`、CtrlCmd録画履歴、録画HTTPの一覧・詳細・Range再生はordinal 0だけを
表す。ワンセグfileを二件目の録画履歴へ増やさず、既存の録画番号と再生URLを変えない。

ワンセグのfile状態はSQLiteのordinal 1と、秘密を含まない運用集計で確認する。本仕様ではワンセグfileの
新しい公開download APIを追加しない。

### OSG-022: 出力と互換表は証拠レベルを分ける

通常出力へ接続先、service ID、予約番号、番組、file名、絶対path、生のprovider応答を追加しない。
ワンセグ成功、部分、欠落の件数と固定理由だけを出せる。

互換実装表は、KonomiTV固定版のsource、合成契約test、実Mirakurun、他の互換提供元を別行または別証拠で
管理する。source確認だけで`LIVE`、`BLACK_BOX`、全体互換へ昇格しない。

## 既存仕様から置き換える範囲

本仕様は次の対象外記述だけを置き換える。既存文書は履歴として残す。

| 既存仕様 | 本仕様で変える点 |
|---|---|
| `bounded-automatic-reservations-v1.md` BAR-007 | 条件を満たすワンセグ規則から予約を作れる |
| `basic-recording-settings-v1.md`の`partial_rec_flag`と`partial_rec_folder` | 0に加えて、flag 1と録画先0～1件を受ける |
| `safe-reservation-output-v1.md`のワンセグ録画先0件 | 本仕様の安全profileで0～1件を受ける |
| `bounded-concurrent-recordings-v1.md`の資源見積り | 一予約につき補助stream、file、buffer、goroutineを最大一つ加える |

強制チューナー、連続file、ぴったり録画、複数の通常録画先、ワンセグだけの録画は対象外のままである。

## 必須test

### CtrlCmdとdomain

- `partial_rec_flag`の0、1、2、255。
- ワンセグ用録画先0件、空一件、通常一件、2件、vector一件超過。
- 正しいplugin、別plugin、大文字違い、空引数、reserved、絶対path、親参照、不正UTF-8、NUL、上限境界と一件超過。
- 単発2013、変更2015、一覧2011、自動予約2131／2132／2134／1033で同じ値を往復する。
- truncated、末尾一byte、構造size、本文上限、巨大countを固定失敗にする。

### service解決と自動予約

- 同じnetwork ID、TSIDで一つの部分受信serviceを選ぶ。
- 候補0件、2件、同じSID、別network、別TSID、未検証、非選択、不正locator。
- 自動予約は候補一件で予約snapshotを作り、他は規則を保存して理由別件数だけを出す。
- 規則変更・削除、snapshot差替え後も作成済み予約が同じlocatorと録画先を持つ。

### SQLite migrationとrepository

- schema第12版のbackup、manifest readback、第13版migration、二度目、失敗、restore、再適用。
- 既存予約はワンセグ無効。行あり、行なし、insert、update、delete、transaction rollback。
- locator、folder、templateの境界、未知column、duplicate、参照不整合、破損値。
- claimで0番だけ、0番と1番、path重複、ordinal 2、途中失敗、再起動後の完全一致。

### file名とexecutor

- 既定名、template名、ワンセグ専用folder／template、`.ts`大小文字、`.oneseg`を含む元名、最大長。
- メインのfile作成、stream open、DB開始が、ワンセグ準備より先である。
- 二つのservice ID、`decode=1`、優先度0、別correlation IDを偽serverで確認する。
- ワンセグの3xx、4xx、5xx、wrong content type、stall、disconnect、早期終了、再接続成功・枯渇。
- ワンセグのcreate、write、sync、close、link、directory sync失敗でもメインを完成させる。
- メイン失敗、利用者停止、親cancel、process shutdownで補助処理を止める。
- body、lease、file、timer、補助goroutineが残らない。raceとshuffleで確認する。

### 完成処理と復旧

- 0番・1番それぞれの`PLANNED`、`WRITING`、`PARTIAL`、`FINALIZED`。
- file sync前後、close前後、`FINALIZING`前後、link後、directory sync後、terminal保存前のcrash。
- メイン完成・ワンセグ部分、メイン完成・ワンセグ欠落、両方完成、両方部分、同名完成file。
- 利用者停止で両方188 byte以上、ワンセグ188 byte未満、ワンセグ不整合。
- 履歴、録画HTTP、Range再生、録画後script、電源動作がメイン一件のままである。

### 資源、build、実環境

- 同時録画数1、2、20以上、`N`の算術境界で最大`2N`接続を越えない。
- `go test ./...`、shuffle、race、`go vet ./...`、module検証、govulncheck。
- Darwin／Linux amd64／arm64のCGO無効build、Hosted Ubuntu CIを同じ最終commitで行う。
- 実Mirakurunで二service stream、同一channel共有、二file、停止、再起動をboundedに確認する。
- 実環境で共有を確認できなければ`NOT RUN: same-channel tuner sharing not verified`と記録する。

## 対象外

- `partial_rec_flag=2`、ワンセグだけの録画。
- 強制チューナー、直接チューナー制御、tuner indexの変換。
- チャンネル全体stream、任意のTS分割、別serviceへのretune、event relay。
- 任意個数の補助出力、複数画質、動的plugin、framework、ORM、code generation。
- ワンセグfile用の新しい公開download API、二件目の録画履歴。
- Node／npm、CGO、新しい外部依存。
- 未確認のKonomiTV／Komorebi／Mirakurun互換提供元への全体互換表明。
