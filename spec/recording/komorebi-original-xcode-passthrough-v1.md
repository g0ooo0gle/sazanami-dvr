# Komorebi向け原画質xcode配信仕様 v1

- Status: Accepted
- Date: 2026-08-11
- Requirements: `KXP-001`～`KXP-015`
- Related decision: ADR-0056
- Target client: Komorebi `1.1.0-beta6` / `41916c63cdf53cc62643333f630bb1095c14a298`
- Target product base: `04fd71ff33ef7f0f1a41f96d151efe19dee7c52b`

## 目的

KomorebiのEDCB直接接続で録画再生方法にHTTP APIを選んだ場合も、完成録画を原画質のまま再生できる
ようにする。画質変換を装わず、既存の再生可能判定、file検証、Range配信、資源上限を再利用する。

## 対象となる呼び出し

対象は固定したKomorebiの次の呼び出しに限る。

| 呼び出し | 固定ソースのpathとsymbol |
|---|---|
| resolverから`ctok`と`option[]`を取得する | `app/src/main/java/com/beeregg2001/komorebi/data/repository/edcb/EdcbRecordRepository.kt::fetchResolverSettings` |
| `fname`または`id`で`/api/xcode`を組み立てる | 同ファイルの`getRecordStreamUrl` |
| option 10を非ライブ再生として扱う | `app/src/main/java/com/beeregg2001/komorebi/viewmodel/VideoPlayerViewModel.kt::resolveStreamUrl` |
| HTTP RangeでMPEG-TSを読む | `app/src/main/java/com/beeregg2001/komorebi/ui/video/player/VideoPlayerManager.kt::rememberManagedExoPlayer` |

固定コミットの`LICENSE`はMITである。確認はsourceの読み取りだけで、外部コード、fixture、通信記録を
製品へコピーしない。

## 要件

### KXP-001: resolverは原画質を一件だけ表示する

引数なしの`GET`／`HEAD /komorebi/resolver.lua`は、次の意味を持つJSONを返す。

- `ctok.xcode`は空文字。
- `ctok.view`は空文字。
- `option`は`{"id":"10","name":"オリジナル（変換なし）"}`の一件だけ。

`Content-Type`は`application/json; charset=utf-8`とする。HEADはGETと同じheaderを返し、本文を返さない。
品質番号2など、実装していない変換optionを表示しない。

### KXP-002: 録画一件のresolver応答を維持する

正の録画番号を一つ指定した`GET`／`HEAD /komorebi/resolver.lua?id={id}`は、KXP-003の再生可能条件を
満たす場合だけ、従来どおり`video_url=/recordings/{id}.ts`と関連資材の仮想URLを返す。
`/api/xcode`のURLをresolverへ直接埋め込まない。未知、重複、不正数値、未知queryは内容を返さない。

### KXP-003: 既存の再生可能判定だけを公開する

配信できるのは、製品の`HistoryItem.Playable()`がtrueを返す主segmentだけとする。対象は、正常終了した
録画と、`PARTIAL/USER_REQUESTED_STOP`として安全に確定した利用者停止の録画である。どちらも履歴が妥当で、
実時間が1秒以上、segmentが`FINALIZED`かつ`FINAL`、三つの完成flagが有効、byte数が188以上でなければ
ならない。

録画中、安全確定前、復旧途中、利用者停止以外の部分録画、失敗、取消し、欠損、不整合を成功へ丸めない。
`/recordings/{id}.ts`と`/api/xcode`へ別々の公開条件を作らない。

### KXP-004: xcodeのmethodを固定する

`/api/xcode`はGETとHEADだけを受ける。他のmethodは405と`Allow: GET, HEAD`を返す。pathの大小文字、
末尾slash、余分なpathを同じものとして扱わない。redirectを返さない。

### KXP-005: fnameは仮想path一件だけを受ける

`fname`分岐は、標準URL decode後に`recordings/{id}.ts`と完全一致する値を一件だけ受ける。`id`は
先頭0のない正の32 bit整数とする。先頭slash、空要素、`.`、`..`、backslash、NUL、trailing文字、
絶対path、URL、EDCBの`video/rec` pathを受けない。

`fname`からhost pathやfile名を直接作らず、録画番号をKXP-003の履歴へ引き直す。

### KXP-006: id分岐を同じ録画へ解決する

`id`分岐は、先頭0のない正の32 bit録画番号を一件だけ受ける。`fname`と`id`は排他的とし、両方の指定、
両方の省略、重複を400 `invalid-query`とする。二つの分岐は同じ再生可能条件とfile配信を使う。

### KXP-007: option 10以外を実行しない

`option=10`を一件だけ必須とする。省略、空、重複、符号、先頭0、2などの別値は400
`unsupported-option`とする。別値を10へ丸めず、外部processを起動しない。

### KXP-008: ctokは空tokenだけを受ける

`ctok`は省略、または空文字一件だけを受ける。非空、重複、NULは400 `invalid-token`とする。
空tokenは固定クライアントとの互換値であり、認証成功を意味しない。

### KXP-009: ofssecは検証だけを行う

`ofssec`は省略、または先頭0のない10進整数`0`～`86400`を一件だけ受ける。負数、小数、指数、空、
符号、先頭0、overflow、重複を400 `invalid-offset`とする。

option 10では`ofssec`を配信開始byteへ変換せず、GETとHEADの内容範囲を変えない。再生再開とシークは
KXP-011のRangeを正本とする。

### KXP-010: 未知queryを拒否する

受理できるquery名は`fname`、`id`、`option`、`ctok`、`ofssec`だけとする。名前は大小文字を区別する。
未知名、空の名前、同じ名前の複数値を400 `invalid-query`とし、一部だけを解釈しない。

### KXP-011: 完成fileをRange対応で返す

成功時は`Content-Type: video/mp2t`、正確な`Content-Length`、`Accept-Ranges: bytes`を返す。全体GET、
HEAD、単一byte Range、206、416はGo標準HTTPの意味に従う。先頭、途中、末尾、一byteを扱う。
複数Rangeは416 `multiple-ranges-unsupported`とする。

HEADはfile条件とRangeをGETと同じように検証するが、本文を返さない。配信内容を一括でmemoryへ読まない。

### KXP-012: file検証と同時数を共有する

`/recordings/{id}.ts`と`/api/xcode`は、同じ`HistoryItem.Playable()`、録画保存先adapterの同じ`OpenFinal`、
同じ完成file配信helper、同じ同時8件のsemaphoreを使う。9件目は待たずに503 `stream-limit`とする。

file descriptorで通常file、所有者、mode、symlinkではないこと、DBのFilePlanとbyte数、差替えがないことを
確認する。欠損、所有者違い、mode違い、symlink、hard link、byte不一致、open後差替えでは内容を返さない。

### KXP-013: 終了経路で資源を閉じる

完了、HEAD、Range、client切断、context取消し、HTTP書込み失敗、内部失敗のすべてでfileと配信枠を
一度だけ解放する。retry、queue、worker、goroutineを追加しない。片方のURLで失敗しても他の配信枠を
失わない。

### KXP-014: 失敗理由と出力を固定する

主なHTTP失敗は、400 `invalid-query`／`unsupported-option`／`invalid-token`／`invalid-offset`、
404 `not-found`／`file-unavailable`、416 `multiple-ranges-unsupported`、503 `history-unavailable`／
`stream-limit`へ固定する。内部error本文を返さない。

通常出力へ録画番号、番組、file名、絶対path、相対path、token、Range、接続元、生の要求・応答を出さない。
公開件数と固定理由だけを診断に使う。

### KXP-015: 依存と対応表明を限定する

実装はGo標準libraryと既存SQLite依存だけを使う。DB migration、新しい依存、CGO、framework、ORM、
code generation、Node／npm、外部実行ファイルを追加しない。製品versionと変更履歴は別のrelease handoffで
更新する。

対応表明は「Komorebi 1.1.0-beta6、EDCB直接接続、HTTP API録画再生、option 10、原画質」までとする。
画質変換、一般的なEDCB WebUI xcode互換、未知版、KonomiTV HTTP APIへの対応を表明しない。

## 必須テスト

- resolver設定のGET／HEAD、JSON content type、option 10一件、token、録画IDあり、未知ID、未知・重複query。
- `fname`の正常、URL encoding、先頭slash、空要素、`.`、`..`、backslash、NUL、絶対path、URL、
  `video/rec`、trailing、ID 0・負数・先頭0・最大・一件超過。
- `id`の正常、0、負数、先頭0、overflow、`fname`との同時指定、両方省略、重複。
- optionの10、省略、空、重複、2、符号、先頭0、overflow。未対応optionでfileを開かないこと。
- ctokの省略、空一件、非空、重複。ofssecの省略、0、正数、86400、86401、負数、小数、指数、
  先頭0、overflow、重複。ofssecで応答byteが変わらないこと。
- GET、HEAD、全体、先頭・途中・末尾・一byte Range、範囲外、複数Range、Content-Length、
  Accept-Ranges、content type、redirectなし。
- 空DB、正常終了、`PARTIAL/USER_REQUESTED_STOP`の安全確定、利用者停止以外の部分録画、失敗した録画、
  安全確定前、復旧途中、再生可能条件の各不一致、破損履歴、欠損file、symlink、hard link、所有者違い、
  mode違い、byte不一致、open後差替え。
- `/recordings`と`/api/xcode`の合計1～8件、9件目拒否、拒否後と全終了後の再利用。
- GET途中、Range途中、client切断、cancel、file open失敗、write失敗でfile descriptorと配信枠が残らないこと。
- resolverでoption 10を選び、`video_url`から`fname`を作り、xcode Rangeへ到達する固定版相当の一連test。
- 既存Native REST、resolver関連資材、直接`/recordings`、CtrlCmd 2017／2024の回帰。
- `go test ./...`、shuffle、race、vet、`go mod verify`、`govulncheck ./...`、CGO無効の
  Linux／Darwin amd64／arm64 build、Hosted Ubuntu CI。

## 実環境確認

公開候補のLinux amd64配布物と隔離data rootを使い、完成録画一件でresolverのoption 10、`fname`分岐、
`id`分岐、全体byte数、先頭・途中・末尾Range、保存fileとのhash一致を確認する。

固定したKomorebiを再現可能に導入し、EDCB接続とHTTP API録画再生を選ぶ。選択肢が原画質一件だけである
こと、再生開始、視聴位置からの再開、30秒前後移動、末尾までの再生を確認する。端末codecに依存する
失敗はSazanamiのHTTP失敗と分ける。実施できなければ`NOT RUN: fixed Android client unavailable`とし、
画面対応済みとは表示しない。

報告には件数、転送byte数、所要時間、HTTP状態、成否だけを残す。接続先、端末名、録画番号、番組、
file名、path、hash値、TS内容、生の応答を残さない。
