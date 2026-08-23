# KonomiTV v0.14.1実用バックエンド完了仕様 v1

- Status: Accepted
- Date: 2026-08-23
- Applies to: Sazanami DVR v0.5.0
- Decision: `docs/adr/0067-konomitv-practical-backend-goal.md`
- Fixed KonomiTV source: tag `v0.14.1` / commit `0a32188274b81c1e7bed642474b208bd2a543a6b`
- Active override: ADR-0069（v0.5.0公開後のchecksum成果物と時間指定耐久試験）

## 目的

完了条件を絞る。
対象を広げない。

Sazanami DVRを固定KonomiTV v0.14.1の日常利用に必要なEDCB互換バックエンドとして公開するため、
最小の完了条件と非対象を固定する。EDCB全体や別clientへの互換性は主張しない。

全操作マトリクス仕様v1は、固定sourceの19種類のCtrlCmd、操作群、試験証拠、既知制限を追跡する資料として
維持する。本仕様は、同仕様の`K5-005`にある全検索条件の実機総当たりと、`K5-012`にある全12操作群の
未実施を残さないrelease gateだけを置き換える。上限、失敗応答、秘匿、録画削除境界は
置き換えない。

## 完了条件

確認対象は、利用者が普段たどる導線である。
コマンド数だけでは判定しない。

### `KB-001` 固定clientと実装範囲

対象は固定sourceから到達する19種類のCtrlCmdと、録画共有folder、KonomiTV公開HTTP APIに限る。
source inventory、tag、完全SHA、path、symbol、除外理由を維持する。呼ばれないcommandを追加しない。

### `KB-002` 番組情報と設定

KonomiTVが起動し、放送局、番組表、検索結果、番組詳細、録画preset、容量、局ロゴを取得できる。
通常利用の要求でSazanami起因のHTTP 5xx、panic、不意の切断を出さない。個別の検索条件はcontract testの
結果を使え、全条件を同じ実験で画面総当たりする必要はない。

### `KB-003` 予約と録画

予約の一覧、追加、変更、開始前取消し、放送途中からの録画開始、録画中停止、自然終了が動く。
片側だけ余白を指定する固定clientのwire表現を受理し、取消し後に予約、録画file、resourceを残さない。

### `KB-004` 自動予約とライブ

キーワード自動予約は、KonomiTVに同梱画面がないため公開HTTP APIで確認する。CRUD、条件評価、
一度だけの予約生成、試験条件と予約の後始末を確認する。

ライブは1073、301、1074を一続きで確認する。TS受信、client切断、停止後の接続数0件を確認する。

### `KB-005` 完成録画

新しい60秒以上の録画一件について、KonomiTVの一覧と詳細、通常取得、単一Range、再起動後読戻し、
再解析、サムネイル再生成を確認する。同じ使い捨て録画を管理APIで削除し、次を読み戻す。

- KonomiTVの録画番組DBと録画fileの対象が0件になる。
- 対応するthumbnailと補助fileが残らない。
- Sazanamiの予約・録画中件数が0件である。
- Sazanami履歴を削除せず、`MISSING / FILE_MISSING`へ収束する。

削除試験のためだけに作る一時管理者は、資格情報をfile、command、environment、log、Gitへ残さず、
正式APIで削除してアカウント0件へ戻せる。

### `KB-006` 証拠

操作ごとにSOURCE-VERIFIED、CONTRACT-VERIFIED、BLACK-BOX-VERIFIED、LAB-VERIFIED、
SCREEN-VERIFIED、NOT RUNを区別する。次を満たす経路は、SCREEN-VERIFIEDがなくてもv0.5.0へ含められる。

1. 固定sourceから要求の到達を確認している。
2. 同じ製品commitのcontractまたは統合testが成功している。
3. 利用者の後続状態を含む同等の公開APIまたはprotocol境界を固定clientの実験環境で確認している。
4. 画面固有の制限を互換表へ明記している。

### `KB-007` v0.5.0公開記録

機能実装commitとrelease commitを分ける。release-prepでは版、版表示test、README、変更履歴、互換表、
本仕様とADR-0067の製品copy以外を変更しない。schema、migration、依存、録画形式、機能codeは変更しない。

candidateと統合後mainのCI成功後にannotated tag `v0.5.0`を作成した。当時のworkflowでprerelease、
Linux amd64／arm64 archive、`SHA256SUMS`、`OCI_IMAGE`、multi-architecture OCI imageを公開し、
公開後にchecksum、archive内容、binary版、VCS revision、dirty状態、OCI digestを読み戻した。

次版はv0.5.0の公開記録を引き継がない。実用リリース品質仕様v1に従い、
`SHA256SUMS`を生成・公開せず、利用者の照合結果も完了条件にしない。

### `KB-008` 表示と非対象

公開互換表は「KonomiTV v0.14.1の日常導線に対応」と表示する。次は対応済みと表示しない。

- EDCB全機能、固定sourceから呼ばれないcommand、別versionのKonomiTV
- Komorebi、Android TV、TvCast、HLS、cast、画質変換、端末codec
- 60秒未満の録画をKonomiTV一覧へ表示すること
- キーワード自動予約のKonomiTV同梱画面
- 全画面・全検索条件の実機総当たり

## 必須検証

- 固定source inventoryと19種類のCtrlCmd router testを同じ製品commitへ対応付ける。
- full、shuffle、race、vet、module verification、既知脆弱性検査、主要四環境build、Container CIを行う。
- 実験環境で予約、放送途中録画、自然終了、ライブ、通常取得、Range、再起動、再解析、thumbnail、削除、
  履歴収束を確認する。
- v0.5.0のversion、tag、Release、公開asset、binaryとOCI imageの来歴を同じrelease commitへ固定した。
- 次版の公開条件は、実用リリース品質仕様v1で確認する。

実施していない検証は`NOT RUN`のまま残す。未実施を広い互換性の根拠にしない。
