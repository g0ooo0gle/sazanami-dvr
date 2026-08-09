# ADR-0036: 番組詳細はversion付きの上限付きmetadataとして保存する

- Status: Accepted
- Date: 2026-08-09
- Decision owners: Project owner
- Delegated reviewer: Codex
- Related: ADR-0015、ADR-0023、ADR-0035、Plan 0040、Plan 0049

## Context

Sazanamiは番組名、概要、時刻、長さ、無料・有料だけを番組リビジョンへ保存している。Mirakurun／mirakcは任意で詳細文、ジャンル、映像、音声も返す。KonomiTVはCtrlCmdの対応する構造からこれらを表示し、検索画面ではジャンルとあいまい検索を利用する。

項目ごとに子テーブルを作ると、migration、join、削除制約、repository処理が増える。現在の検索は完成済み番組表を順に読むため、ジャンルのDB indexは必要ない。生JSONを保存するとprovider形式がdomainとDBへ漏れ、canonical hashも不安定になる。

既存の番組リビジョンは変更禁止であり、既存canonical encoding v1の意味を変えてはならない。

## Decision

詳細文、ジャンル、映像、音声をproviderに依存しない型へ変換する。件数、文字列、数値、合計byte数を検証したversion付きcanonical binaryを作り、`program_revisions`へnullableな`metadata`列を一つ追加して保存する。生JSONとprovider固有文字列は保存しない。

metadataが空なら既存canonical encoding v1とhashを使う。metadataがある場合だけcanonical encoding v2を使い、基本項目とmetadataの両方をhashへ含める。v1の関数と既存行は変更しない。metadataの順序は意味順へ正規化し、JSON objectやmapの走査順に依存させない。

上限はmetadata全体256 KiB、詳細64項目、ジャンル64件、音声16件とする。詳細の見出しは各4 KiB、本文は各64 KiBとし、全体上限を先に適用する。言語コードは各音声2件までとする。空配列と項目欠落は同じ「情報なし」へ正規化する。

CtrlCmd 1029と1025は同じ変換を使い、存在する詳細構造だけを出力する。番組検索はジャンル、ジャンル除外、映像、音声を固定EDCBの数値規則で判定する。あいまい検索は文字幅とかなを正規化し、検索語長の25%以下の編集距離を持つ部分文字列を一致とする。除外語へあいまい検索を適用しない。

既存の番組数、本文、構造、文字列、処理期限、同時一件、二回走査の上限は緩めない。失敗または途中までの番組表世代は公開しない。

## Consequences

一つのmigrationと一つのmetadata codecで番組詳細を拡張できる。今後、シリーズなど別の任意項目を追加する場合も、metadataの次versionとして検討できる。通常の番組読取りではdecodeが一回増えるが、全件配列や追加joinは増えない。

詳細が初めて得られた番組は新しいリビジョンになる。録画予約が保持する既存リビジョンは変更せず、番組表の現在世代だけが新しい詳細を参照する。

## Rejected alternatives

### 項目ごとの子テーブル

不採用。現在の読取りと検索にはindexが不要で、repositoryとmigrationが複雑になる。

### 生のMirakurun JSON

不採用。provider形式、未知field、object順序をdomainの正本へ持ち込むことになる。

### 既存canonical encoding v1の変更

不採用。既存hashの意味と復旧可能性を壊す。

### 外部Unicode正規化library

不採用。今回必要な互換範囲は小さく、Go標準ライブラリと固定変換だけで実装できる。新しい依存を増やさない。

## Security and privacy

番組詳細は放送由来の非信頼入力として扱う。通常ログ、エラー、planningの実験結果へ番組名、詳細、ジャンル名、生JSONを出さない。metadataは固定上限、厳格decode、末尾なしを必須にし、破損時はその読取りを失敗させる。
