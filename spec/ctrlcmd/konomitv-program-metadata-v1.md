# KonomiTV番組詳細・全検索条件仕様 v1

- Status: Accepted
- Date: 2026-08-09
- Requirement prefix: `PMETA`
- Related: ADR-0035、ADR-0036、Plan 0049

## 目的

Mirakurun／mirakcの任意番組情報を完成済み番組表へ保存し、KonomiTV v0.14.1が使うCtrlCmd 1029と1025へ返す。ジャンルとあいまい検索を追加し、番組検索画面の全条件を扱う。

## 要件

### PMETA-001: Provider入力

Mirakurun adapterは`extended`、`genres`、`video`、`audios`がない番組を従来どおり受理する。存在する場合は型、重複key、数値範囲、文字列、配列、入れ子、論理要素数を厳格に検証する。未知fieldは既存規則で読み飛ばす。既知fieldの不正を無視せず、sync全体を失敗させる。

### PMETA-002: Domain型

ProviderのJSON型をdomainへ持ち込まない。詳細は見出しと本文、ジャンルは4つの分類値、映像はstream contentとcomponent type、音声はcomponent type、component tag、主音声、sampling rate、最大2つの言語コードとして保持する。

### PMETA-003: 上限

Metadata全体はcanonical encodingで256 KiB以下とする。詳細64項目、ジャンル64件、音声16件、音声ごとの言語2件を上限とする。詳細の見出しは4 KiB、本文は64 KiB以下とする。分類値とcomponent値は8 bit、sampling rateはMirakurun固定版が定義する値だけを受理する。

### PMETA-004: Canonical encoding

Metadataはversion prefix、presence、件数、固定順field、長さ付きUTF-8の順で決定的に符号化する。詳細は見出し順、同じ見出しは本文順、ジャンルと音声は数値field順へ整列し、完全な重複を除く。decodeはprefix、上限、UTF-8、値、件数、順序、末尾なしを再検証する。

Metadataが空なら保存値を`NULL`とし、既存revision v1 hashを維持する。Metadataがある場合は基本項目とcanonical metadataをrevision v2 hashへ含める。

### PMETA-005: SQLite migration

`program_revisions`へnullableなmetadata BLOB列を追加する。既存行を更新せず、insert-only triggerを維持する。保存、現在世代の全読取り、予約照合、追従、backup、restore、旧DBからのmigrationで追加列を扱う。BLOBの破損は空情報へ丸めない。

### PMETA-006: 番組詳細

CtrlCmd EventInfoの`ext_info`は、詳細がある場合だけ出力する。各項目を`- 見出し`、改行、本文の順に連結し、KonomiTV固定版が項目へ分解できる形にする。見出し内の改行は空白へ置換し、本文のCRは除く。変換後も既存文字列上限を守る。

### PMETA-007: ジャンル

`content_info`は、各ジャンルの大分類、中分類、利用者定義分類をEDCBのContentDataへ変換する。空なら構造自体を出力しない。1025のジャンル条件は、大分類が一致し、中分類`0xFF`なら大分類全体、それ以外は中分類も一致する番組を選ぶ。拡張分類は利用者定義分類も照合する。`0xFF / 0xFF`はジャンル情報なしを表す。ジャンル除外は一致結果を反転する。

### PMETA-008: 映像と音声

`component_info`と`audio_info`はmetadataがある場合だけ出力する。映像と音声の検索条件は`stream_content << 8 | component_type`で照合する。条件がありmetadataがない番組は一致させない。Mirakurunのsampling rateはEDCBの固定値へ変換し、変換できない値を推測しない。

### PMETA-009: あいまい検索

あいまい検索は正規表現ではない検索語だけへ適用する。検索対象と検索語について、全角ASCII、空白、ひらがな・カタカナ、半角カナと濁点・半濁点を決定的に正規化する。大文字小文字を区別しない場合はASCII英字を同一視する。

検索語とのLevenshtein距離が検索語のrune数の25%以下になる部分文字列があれば一致とする。計算は2行の整数配列だけを使い、検索語や対象全体に比例する行列を作らない。除外語、正規表現へあいまい検索を適用しない。

### PMETA-010: CtrlCmd共通変換

1029と1025は、EventInfoの大きさ計算と出力を共有する。二回の走査でmetadataを含む件数、byte数、順序、hashが変わった場合は完全な成功frameを返さない。

### PMETA-011: 完成済み世代

検索と番組表は要求開始時に固定した完成済み世代だけを読む。画面操作からMirakurun／mirakcへ接続しない。取得失敗中は直前の完成済み世代を維持する。

### PMETA-012: 互換表示

公開互換表は、通常検索、ジャンル検索、あいまい検索、番組詳細、映像、音声を分けて表示する。自動testだけの項目を実環境確認済みにしない。製品versionは0.0.12とする。

### PMETA-013: 依存と変更範囲

実装はGo標準ライブラリと既存依存だけを使う。新しいdependency、CGO、framework、ORM、code generation、Node／npm、外部processを追加しない。予約、録画、録画file、ライブ視聴の意味を変えない。

### PMETA-014: Privacy

番組名、概要、詳細、ジャンル名、言語、検索語、生JSON、CtrlCmd要求・応答を通常ログ、エラー、Git、実験結果へ記録しない。失敗は固定した理由、件数、byte数だけで報告する。

## 必須テスト

- Mirakurunの項目欠落、空、全項目、unknown field、duplicate key、null、型違い、数値overflow、文字列・配列・入れ子・metadataの上限と一件超過。
- Provider cursorの256件境界、close、cancel、truncated、disconnect、stall、本文上限、failed generation、previous completed generation維持。
- Metadata canonical encodingの順序差同一hash、重複除去、v1 hash維持、v2変更、全上限、末尾、破損、再起動後読取り。
- 旧DB migration、新規DB、既存行NULL、insert-only、backup、restore、予約・追従・録画の回帰。
- 1029と1025の詳細、ジャンル、映像、音声構造を固定KonomiTV byte reader相当で確認する。
- ジャンルの大分類、中分類全体、拡張、ジャンルなし、除外、映像、音声、metadataなしを確認する。
- あいまい検索の完全一致、一文字差、挿入、削除、25%境界、一件超過、全角ASCII、かな、半角カナ、濁点、case、title-only、除外語、正規表現との分離を確認する。
- 262,144件、256 MiB、一byte超過、二回走査差、取消し、同時要求、DB・書込み失敗、全件配列なしを確認する。
- 通常command、予約、自動予約、録画、再生、ライブ視聴の回帰を確認する。
- `go test ./...`、shuffle、race、vet、`go mod verify`、`govulncheck ./...`、CGOを使わないLinux／Darwin amd64／arm64 build、Hosted Ubuntu CI。

## 実験環境

公開Linux amd64配布物を隔離data rootへ導入する。番組表同期後、KonomiTV v0.14.1で番組詳細、ジャンル、映像、音声が得られる件数だけを確認する。検索画面ではジャンル、ジャンル除外、あいまい検索を確認する。番組名、詳細、検索語、放送局、接続先、生の要求・応答は記録しない。
