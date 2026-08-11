# 起動時チューナー数を使う同時録画既定値仕様 v1

- Status: Accepted
- Date: 2026-08-11
- Requirements: `TCD-001`～`TCD-010`
- Related ADR: ADR-0050
- Replaces: 上限付き複数同時録画仕様v1の`BCR-001`、`BCR-003`、`BCR-006`、`BCR-010`の
  既定値1、最大8、上限比例の事前確保に関する部分

## 目的

明示値を最優先したまま、未指定時にはMirakurunの設定済みチューナー数を同時録画の初期上限にする。
空きチューナーの予測や実行中の上限変更は行わず、既存のDB確保、録画ごとの分離、失敗理由を維持する。

本仕様は、上限付き複数同時録画仕様v1のうち、既定値1、最大8、上限値と同じ容量の完了通知、
最大8を前提にしたprovider接続、資源上限8だけを置き換える。DB確保、枠不足、録画ごとの分離、
停止、永続化、既存公開契約に関する`BCR-002`～`BCR-010`の要件は、置換対象を除いて維持する。
同時録画数について両仕様が異なる場合は、本仕様を新しい差分として適用する。

## 要件

### TCD-001: 明示値を最優先する

`recording serve`は既存の`--max-concurrent-recordings`を維持する。flagが明示された場合は、正の
整数を同時録画上限としてそのまま使い、自動判定用のnetwork要求を行わない。0、負数、数値でない値、
実行環境の整数範囲を超える値、余剰引数は、DB、録画保存先、networkを開く前に安定した理由で拒否する。

### TCD-002: 未指定時だけ一度取得する

flagが未指定の場合だけ、`recording serve`の起動時に利用者が明示したMirakurun base URLへ
`GET /api/tuners`を一度実行する。実行中の再取得、polling、自動retryを行わない。この要求で
チューナーやstreamを開かない。

### TCD-003: HTTP境界を固定する

要求は`Accept: application/json`とし、HTTP 200、`application/json`、identityまたは無指定の
content encodingだけを成功候補にする。redirect、proxy環境変数、暗黙のcredential、TLS検証無効、
応答圧縮を使わない。応答headerは64 KiB、本文は1 MiB、接続と応答開始は5秒、操作全体は5秒を
上限とする。Content-Lengthとchunkedのどちらも本文上限を一byte超えた時点で失敗させる。

### TCD-004: 配列を逐次数える

応答はtop-level JSON arrayを必須とし、全配列をsliceへ読み込まない。各要素を一つずつ読み、
JSON object一要素を一台として数える。未知fieldは許容する。object以外の要素、重複key、不正UTF-8、
深さ32超、未知構造4,096 token超、truncated、末尾token、件数の整数overflowは不正応答とする。

### TCD-005: fieldから台数を推測しない

一objectを常に一台として扱う。`types`の要素数、name、command、process ID、利用者一覧、
`isAvailable`、`isRemote`、`isFree`、`isUsing`、`isFault`などの有無や値で件数を増減しない。
field欠落と未知fieldは、objectとして正しく完結している限り件数へ影響させない。

### TCD-006: 失敗時は一件へ戻す

正常に最後まで読んだ件数が一件以上なら、その件数を有効値にする。空配列、HTTP／JSON失敗、
上限超過、操作期限超過、切断では、途中件数を捨てて有効値1にする。取得失敗だけを理由に録画
サービスの起動を失敗させない。プロセス終了による親contextの取消しはfallbackへ変換せず、起動を
中止する。応答bodyと再利用待ち接続は必ず閉じる。

### TCD-007: 台数へ固定上限を設けない

明示値と正常に取得した件数には、8、20、32などの固定上限を設けない。20以上では、録画ごとの
network、buffer、file descriptor、Go routineが増える可能性を、起動時に一度だけ注意する。
注意によって値を変更しない。注意には接続先、チューナー名、利用者、番組情報を含めない。

### TCD-008: 起動時の確保を設定値から分離する

scheduler、完了通知、停止通知、Mirakurun stream adapterは、設定値に比例するchannel、slice、map、
接続、録画bufferを起動時に確保しない。設定値は正の整数として保持し、資源は実際に開始した録画数に
応じて増やす。非常に大きい正の明示値でも、起動時メモリをその値に比例して増やさない。

### TCD-009: 同じ有効値を録画経路へ渡す

決定した有効値をschedulerと録画用Mirakurun stream adapterへ同じ値で渡す。DB確保後だけ録画を
開始し、実行中件数は有効値を超えない。Mirakurunのstream割り当てを最終判断とし、拒否された録画
だけを既存の安定した理由で終了する。先着録画の中断、予約優先度による割込み、同一serviceの
stream共有、ライブ中継上限の変更を行わない。

### TCD-010: 永続化と公開契約を変えない

DB schema、録画file形式、CtrlCmd、Native REST API、KonomiTV／Komorebiの公開形式、外部依存を
変更しない。通常出力は採用した件数、fallback、20以上の注意、安定した理由だけを扱い、base URL、
絶対path、チューナー情報、生の要求・応答を出さない。

## 必須テスト

### CLIと起動判定

- flag未指定と、`--max-concurrent-recordings`を明示した1、8、9、19、20、21、大きい正数。
- 明示した全正常値で`/api/tuners`要求が0回であること。
- 0、負数、非数、整数overflow、余剰引数を外部資源を開く前に拒否すること。
- 未指定の成功件数1、19、20、21と、20以上の注意が一度だけであること。
- 空配列と全失敗で有効値1になり、取得失敗だけでは起動を拒否しないこと。

### Mirakurun HTTP／JSON

- 1件、複数件、空配列、objectの空、未知field、未知の入れ子、複数`types`、全瞬間状態の組合せ。
- object以外、duplicate key、不正UTF-8、深さとtokenの上限一件超過、末尾token、truncated。
- wrong methodを送らないこと、3xx／4xx／5xx、wrong content type、圧縮応答、bodyなし。
- Content-Lengthの上限ぴったり／一byte超過、chunkedの上限ぴったり／一byte超過。
- 接続失敗、応答header stall、本文stall、途中切断、操作期限で一件へ戻ること。
- 親contextの取消しではfallbackせず起動を中止すること。
- 全経路のbody close、再利用接続close、file descriptorとGo routineの残留なし。
- 一覧全体を保持せず、入力件数を増やしても保持メモリが全objectの合計に比例しないこと。

### Schedulerとstream

- 有効値1の従来動作、9件以上を受け付けること、上限内の開始、一件超過の枠不足。
- 大きい正の設定値でconstructorと起動準備が完了し、設定値に比例するchannel／mapを作らないこと。
- 一件完了後の枠再利用、停止通知、親cancel、基盤失敗で全実行を待つこと。
- 録画stream adapterが同じ有効値を受け取り、設定値だけではHTTP接続を作らないこと。
- provider拒否が一件だけを終了し、先に始まった録画を中断しないこと。
- ライブ中継、番組表、予約、自動予約、録画後処理、録画履歴、再生の既存上限と動作の回帰。

### 全体

- 対象packageの短いtestを先に行い、通常、shuffle、race、`go vet ./...`、`go mod verify`、
  `govulncheck`、CGO無効のLinux／Darwin amd64／arm64 buildを最終製品commitで行う。
- Hosted Ubuntu CIを最終製品commitで成功させる。
- 実験環境では公開配布物を使い、未指定起動で一覧取得が一回だけであること、有効値、所要時間、
  fallback有無だけを記録する。チューナー名、放送情報、接続先、生の応答は記録しない。
- 実streamを開かない確認はチューナー能力の成功として扱わない。実録画を追加しない場合は
  `NOT RUN: tuner allocation not exercised`と記録する。

## 対象外

空きチューナー予測、放送種別ごとの割り当て、同一周波数共有、運転中の上限変更、予約優先度、
実行中録画の中断、直接チューナー制御、DB migration、新しい外部依存。
