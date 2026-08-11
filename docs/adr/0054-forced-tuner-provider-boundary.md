# ADR-0054: 強制チューナーは保存し、Mirakurun録画へ適用しない

- Status: Accepted
- Date: 2026-08-11
- Deciders: Project owner（v1.0までの技術判断をCodexへ明示委任）
- Delegated reviewer: Codex
- Related: ADR-0023、ADR-0032、Plan 0069、Handoff 0044
- Product copy path: `docs/adr/0054-forced-tuner-provider-boundary.md`
- Product sync state: NOT COPIED
- Supersedes: None
- Superseded by: None

## 背景

KonomiTV v0.14.1の`forced_tuner_id`は、CtrlCmdでは`tuner_id`として送られる。`null`または0は自動選択、
0以外は指定値を表す。KonomiTVの同梱画面では非表示だが、HTTP schemaと予約・自動予約の変換処理に
含まれる。

Sazanamiは、自動予約条件の非zero `TunerID`をSQLiteへ保存し、追加、一覧、変更、再起動後の一覧で
読み戻せる。自動予約評価はその規則を利用不能として扱い、予約を作らない。単発予約では既存の
`recording-setting-out-of-profile`を返す。

Mirakurun 4.1.3の`GET /api/services/{id}/stream`にはチューナー選択の入力がない。`GET /api/tuners`は
状態とindexを返すだけで、選んだindexへサービスストリームを割り当てる操作ではない。EDCBの番号と
Mirakurunのindexを対応付ける契約もない。

Project ownerは、v1.0までの技術判断と必要なDB・公開API変更をCodexへ明示委任した。本判断はその委任、
KonomiTVとMirakurunの固定source、製品baseの読取り結果に基づいてAcceptedとする。

## 判断

強制チューナー指定は、自動予約条件の保存・読戻しだけを対応範囲とする。非zero値を含む規則から
予約を作らず、Mirakurunが指定どおりのチューナーを使ったとは表明しない。

利用不能理由は`forced-tuner-not-supported-by-provider`へ固定する。既存の`unavailable_rules`合計を
維持しながら、理由と件数だけをsanitized出力へ追加する。規則番号、検索語、番組、service、endpoint、
利用者、path、生の設定値は出力しない。

公開文書では次の意味を、利用者向けの説明として維持する。

> 強制チューナー指定はMirakurunの公開APIでは利用できません。自動予約条件の値は保存して一覧・変更で
> 返しますが、この値を含む条件から予約は作成しません。チューナーの自動選択を続けたい場合は、
> 強制指定を解除してください。

互換実装表では「条件の保存・読戻しのみ対応。録画への適用は非対応」と表示する。source確認を
実通信や製品対応の証拠へ昇格させない。

単発予約の非zero `tuner_id`は、引き続き要求全体を対象外として失敗させる。値を0へ丸めて成功させない。
将来用のprovider interfaceや設定flagは追加せず、選択能力を持つproviderが現れた時点で別ADRにする。

## 結果

- KonomiTVが持つ自動予約条件を失わず、一覧と変更で返せる。
- 適用できない指定を黙って自動選択へ変えない。
- 利用者は予約が作られない理由と、設定を直す方法を公開文書から判断できる。
- 製品変更は利用不能理由、理由別件数、test、公開文書に限られる。
- 強制チューナーを必要とする運用には対応しない。

## 採用しなかった案

### Mirakurunのtuner indexへ同じ数値で置き換える

二つの番号体系を結び付ける契約がなく、サービスストリームにもindexを指定できないため採用しない。

### 指定を無視して自動選択で予約する

録画が成功しても、利用者の明示設定と異なる。設定を適用したように見せないため採用しない。

### `/api/tuners`を監視して空いた番号を待つ

読取り結果と次のstream割当ての間に競合があり、指定にもならない。pollingと新しい失敗経路も増えるため
採用しない。

### Mirakurunを介さずチューナーを直接開く

providerの割当て、復号、同時利用を壊し、Sazanamiの責任範囲を越えるため採用しない。

## 検証

- 非zero `TunerID`を含む自動予約条件の追加、一覧、変更、再起動後の一覧が完全に一致する。
- 非zero値の規則は番組へ一致しても予約を作らない。
- zero値の規則は従来の自動選択として評価される。
- 理由と件数が固定順で出力され、秘密、規則番号、検索語、番組情報を含まない。
- 単発予約の対象外応答、同時録画、provider stream、SQLite schemaを変更しない。
- 固定SHAのsource確認を`SOURCE`として記録し、実通信済みと表現しない。

## 製品同期

- Handoff: Handoff 0044
- Planning source commit: Handoff 0044で固定する
- Target product base commit: `75b416bcf310458d0526d9bdf1599dedcc4d8033`
- Product destination: `docs/adr/0054-forced-tuner-provider-boundary.md`
- Last synchronized product commit: NOT COPIED
- Known divergence: 製品文書は強制チューナー未対応と記すが、固定理由と利用者向け説明はまだない

## 見直す条件

- providerが公開APIで、安定したチューナー識別子とstreamへの選択指定を提供する。
- KonomiTVが強制チューナーのfieldまたは意味を変更する。
- 実EDCBとMirakurunの間に、再現可能で公開された番号対応が追加される。
