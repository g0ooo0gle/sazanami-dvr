# 完成済み録画の一覧・詳細・直接再生仕様 v1

- Status: Accepted
- Date: 2026-08-09
- Requirements: `CRR-001`～`CRR-012`
- Related ADR: ADR-0033

## 目的

録画結果を読み取り専用APIで確認できるようにし、Komorebi 1.1.0-beta6から完成済み録画の一覧、詳細、直接再生を一つの流れで利用できるようにする。

## 要件

### CRR-001: 録画履歴の正本

履歴は既存SQLiteの予約、CtrlCmd録画番号、録画試行、先頭segmentから読む。新しいtableや別indexを追加しない。公開IDは正のCtrlCmd録画番号とし、内部UUIDを返さない。

一件は公開ID、状態、終了理由、予定開始・終了、実開始・終了、byte数、番組名、局名、ONID、TSID、SID、EID、完成ファイルの有無を持つ。内部path、owner ID、finalization tokenは公開型へ含めない。

### CRR-002: Native REST API

`GET /api/recordings`は録画番号の新しい順に、成功、失敗、一部保存、中止、開始できなかった終了結果を返す。`limit`は省略時50、1～100とする。任意の`before`は正の録画番号一つだけを受け、その番号より小さい項目を返す。未知query、重複query、不正数値、範囲外を400とする。

`GET /api/recordings/{id}`は一件を返し、未知IDは404とする。GETとHEAD以外は405と`Allow`を返す。JSONはUTF-8、未知fieldを追加できるが、既存fieldの意味を変えない。

### CRR-003: Komorebiへ公開する項目

CtrlCmdとresolverへ返せるのは、録画試行が`SUCCEEDED`、終了理由が正常終了、segmentが`FINALIZED`かつ`FINAL`、完成処理の三つのflagが有効、byte数が1以上の項目だけとする。DB破損や条件不一致を成功へ丸めない。

### CRR-004: CtrlCmd 2017

要求本文はCmd2 version 5のU16だけを受ける。応答本文はversion 5と録画済み情報vectorを返す。空一覧を許可する。最大16,384件とし、257件以上でもDBを256件ずつ読み、全件sliceを作らない。件数または応答本文が上限を超える場合は、headerを書き始める前に安定した失敗へ変換する。

### CRR-005: CtrlCmd 2024

要求本文はCmd2 version 5のU16と正のI32録画番号を受ける。完成録画が存在する場合だけversion 5と一件の録画済み情報を返す。未知、未完了、失敗、部分file、欠損は安定したnot-found失敗とする。

### CRR-006: 録画済み情報の変換

CtrlCmd録画済み情報は、録画番号、仮想path、番組名、実開始、実時間、局名、ONID、TSID、SID、EID、drop 0、scramble 0、録画済みを示す非zero状態、予定開始、空comment、空program info、空error info、protect falseをこの順で返す。

実開始・終了が妥当でない項目は公開しない。実時間は1秒以上24時間以下とする。仮想pathは`/recordings/{id}.ts`以外を返さない。

### CRR-007: Resolver

`GET /komorebi/resolver.lua`は`ctok.xcode`、`ctok.view`、空の`option`を持つJSONを返す。tokenは空文字であり、認証機能として表示しない。

正の`id`を一つ指定した場合だけ、完成録画に対する`video_url`、`thumbnail_url`、`chapter_url`、`chapter_alt_url`、`tile_image_url`、`tile_json_url`を返す。未知、重複、未知query、不正数値は4xxとする。絶対URL、絶対path、DB相対pathを返さない。

### CRR-008: 直接再生

`GET /recordings/{id}.ts`とHEADは、CRR-003を満たす完成ファイルだけを`video/mp2t`で返す。録画保存先adapterはDBのFilePlanを再検証し、owner-onlyの通常fileで、symlinkではなく、DBのbyte数と一致することを開いたfile descriptorで確認する。

全体取得と単一byte Rangeを受け、`Content-Length`、`Accept-Ranges`、206、416を標準HTTPの意味で返す。複数Rangeは拒否してよい。未知ID、欠損、差替え、不一致は内容を返さない。client切断とcontext取消しでfileを閉じる。

### CRR-009: 最小の関連資材

resolverが返す画像URLは、完成録画の存在を確認してから製品が生成する中立なPNGを返す。チャプターURLは空の200応答、tile JSONは一枚分の最小情報を返す。実サムネイル、実チャプター、実タイル対応として表示しない。

### CRR-010: HTTP待受

録画常駐processはHTTP接続口を同じprocess内で所有する。既定はnumeric loopbackとする。利用者がnumeric private IPを明示した場合だけLAN待受を許可する。hostname、空host、unspecified、multicast、link-local、global IP、port 0を拒否する。

HTTPはheader 16 KiB、header期限5秒、要求読取10秒、idle 30秒、同時録画配信8件を上限とする。待ちqueueは作らず、上限到達時は503を返す。

### CRR-011: 失敗と秘匿

読み取り処理はDB、予約、録画状態、録画fileを変更しない。通常出力は開始addressの公開範囲と件数だけを返し、番組名、局名、録画番号、接続元、絶対path、相対path、生の要求と応答を出さない。利用者向けHTTPエラーは固定reasonとし、内部error本文を返さない。

### CRR-012: 依存と版

実装はGo標準libraryと既存SQLite依存だけを使う。新しい依存、CGO、framework、ORM、code generation、Node／npm、外部processを追加しない。製品versionをv0.0.9へ進める。

## 必須テスト

- DBの空、全終了状態、完成条件の各不一致、順序、cursor、page一件超過、全件上限一件超過、破損値、cancel。
- 2017と2024の正常、空、未知ID、未完成ID、version、truncated、trailing、整数overflow、応答上限、途中write、cancel。
- Native RESTのGET、HEAD、405、未知path、query重複、未知query、limit、before、JSON escape、応答上限、cancel。
- resolverの設定、一件、未知ID、query重複、未知query、ID overflow、JSON content type、path秘匿。
- 完成fileの全体、HEAD、先頭・中間・末尾Range、範囲外、複数Range、同時8件と9件目、切断、cancel、file close。
- symlink、所有者違い、mode違い、byte不一致、欠損、open後差替えで内容を返さない。
- 1件、256件、257件で全履歴sliceを作らず、応答前のsize計算と実出力が一致する。
- 起動、HTTPだけの失敗、CtrlCmdだけの失敗、正常停止でgoroutineとfile descriptorを残さない。
- `go test ./...`、shuffle、race、vet、`go mod verify`、`govulncheck ./...`、CGO無効のLinux／Darwin amd64／arm64 build、Hosted Ubuntu CI。

## 実環境確認

公開配布物と隔離data rootを使い、完成録画一件のNative REST一覧・詳細、2017、2024、resolver、全体byte数、先頭Range、末尾Rangeを確認する。取得byteのhashを保存済みfileと照合する。番組名、局名、録画番号、path、接続先、TS内容は報告へ残さない。

Komorebi 1.1.0-beta6をAndroid TVへ再現可能に導入できる場合は、録画済み一覧、詳細、直接再生開始、seekを確認する。導入できない場合は`NOT RUN: client installation unavailable`とし、対応済みとは表示しない。
