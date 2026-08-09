# KonomiTV・Komorebi向け局ロゴ中継仕様v1

- Artifact status: Accepted
- Accepted: 2026-08-10
- Human authorization: Project ownerはv1.0までの固定クライアント互換と実験環境の技術判断をCodexへ委任している
- Related ADR: `docs/adr/0040-bounded-service-logo-relay.md`
- Target clients: KonomiTV `v0.14.1`、Komorebi `1.1.0-beta6`
- Provider baseline: Mirakurun `4.1.3`。実行時は文字列ではなく必要なHTTP能力で判定する

## 目的

完成済み番組表にある選択済みサービスの局ロゴを、KonomiTVの2060とKomorebiのHTTPへ同じ安全な境界で返す。ロゴがない場合はクライアントの標準画像へ戻し、他の操作を失敗させない。

## 要件

| ID | 要件 |
|---|---|
| `LOGO-001` | 2060で固定したロゴ対応表、索引、個別PNGだけを返す |
| `LOGO-002` | `GET`／`HEAD /legacy/logo.lua`で同じサービスのPNGを返す |
| `LOGO-003` | 現在公開中の完成済みスナップショットで一件だけ照合できるサービスに限定する |
| `LOGO-004` | Mirakurun取得を5秒、同時4件、一件2 MiB、ヘッダー64 KiBに制限する |
| `LOGO-005` | 任意path、任意URL、proxy、redirect、圧縮、TLS検証無効を使わない |
| `LOGO-006` | ロゴ本文、放送ID、サービス名、接続先、生の応答を通常ログと永続領域へ残さない |
| `LOGO-007` | 取得失敗を番組表、予約、録画、ライブ、既存HTTPの失敗へ広げない |

## KonomiTV向け2060

既存のコマンド番号2060、Cmd2バージョン5、要求512バイト、応答8 MiBを維持する。既存の`Bitrate.ini`と`EpgTimerSrv.ini`は一件要求で固定データを返し、ロゴ提供元へ接続しない。

ロゴでは次の二つの要求だけを追加する。

1. 文字列配列が`LogoData.ini`、`LogoData\\*.*`の順に2件ある要求
2. 文字列配列が`LogoData\\<生成済みファイル名>`の一件だけある要求

2件要求の応答も同じ順の`FileData`配列2件とする。`LogoData.ini`はUTF-8 BOM、CRLFで、選択済みサービスごとに`NNNNSSSS=ID`を一行持つ。`NNNN`と`SSSS`はネットワークIDとサービスIDの大文字4桁16進、`ID`は十進のロゴIDである。

ロゴIDはサービスIDが0～4,095の場合だけ同じ値を使う。同じネットワークIDとロゴIDが重複するサービス、放送IDが重複するサービス、検証済みMirakurun locatorを持たないサービスは対応表と索引へ載せない。4,096サービスの既存上限を超えない。

索引はUTF-8 BOMなし、CRLFとし、一サービス一行を`0 0 0 NNNN_III_000_05.png`とする。`NNNN`はネットワークIDの大文字4桁16進、`III`はロゴIDの大文字3桁16進である。この固定形式以外の名前は受け付けない。

個別ロゴ要求は、要求開始時に固定した同じスナップショットの生成名へ完全一致させる。一件だけ一致したMirakurun locatorをproviderへ渡す。PNGを取得できた場合は要求名と同じ一件の`FileData`を返す。取得できない場合はresult 203、本文0バイトとする。

次をresult 203、本文0バイトにする。

- ロゴ2件の順序違い、欠落、重複、余分な名前
- 大文字・小文字の違い、別の区切り、`..`、NUL、生成していない名前
- 未知または重複したサービス、ロゴID範囲外
- Mirakurunの404、503、不正応答、通信失敗

壊れたframe、extent、本文上限、期限切れ、取り消し、応答書き込み失敗は既存codecの安定分類で接続を閉じる。要求内容やprovider errorを応答へ含めない。

## Mirakurunロゴ境界

provider locatorは、完成済みスナップショットに保存された正規の十進MirakurunサービスIDだけを受ける。URLは設定済みbase URLへ`/api/services/{id}/logo`を追加して作り、利用者のファイル名やqueryを使わない。

成功条件は次のすべてである。

- HTTP 200
- `Content-Type: image/png`。parameterは受け付けない
- 本文が1～2,097,152バイト
- 5秒以内に本文末尾まで読み取れる

404と503はロゴなしとする。3xx、その他の4xx／5xx、未知status、不正content type、空本文、宣言または実測の上限超過、途中切断、stallは利用不能とする。`Content-Length`がなくても上限付きで読み、上限を1バイト超えた時点で失敗する。response bodyはすべて閉じる。

専用adapter全体で実行中4件までとする。5件目以降はrequest contextの期限内だけ待ち、取り消されたら接続しない。生成時、自動起動時、番組表更新だけではロゴAPIへ接続しない。

## Komorebi向けHTTP

pathは`/legacy/logo.lua`との完全一致だけを受ける。methodはGETとHEADだけとする。queryは`onid`と`sid`をそれぞれ一つだけ持ち、余分なkey、重複、空値を拒否する。値は先頭0なしの十進で1～65,535とする。

現在の完成済みスナップショットから、network IDとservice IDが一致するサービスを探す。一件だけならMirakurun locatorをproviderへ渡す。0件または2件以上ならproviderへ接続せず404を返す。

PNGを取得できた場合は200、`Content-Type: image/png`、正確な`Content-Length`を返す。HEADはGETと同じ検証を行い、本文を送らない。ロゴなしは404、通信失敗や不正なprovider応答は503、query不正は400、method違いは405とする。本文は固定した短い理由だけとし、内部errorを含めない。

## 必須テスト

- 既存2つの固定INIが以前と同じ1件応答であり、provider接続0回である。
- 2件ロゴ要求、0／1／4,096サービス、対応表、索引、個別PNGを最後までdecodeする。
- 名前の順序、重複、大小文字、区切り、traversal、NUL、未知名、ロゴID範囲と重複を確認する。
- 200 PNG、404、503、3xx、4xx、5xx、content type、空本文、2 MiBぴったり／一件超過、chunked一件超過、truncated、stall、cancel、同時4／5件、body close、goroutine leakを確認する。
- HTTPのGET、HEAD、queryの欠落・重複・余分、十進境界、未知・重複サービス、method違い、提供元失敗を確認する。
- full、shuffle、race、vet、`go mod verify`、`govulncheck`、CGO-off四環境build、Hosted Ubuntu CIを最終製品commitで通す。

## 実環境確認

公開Linux amd64配布物を許可済み実験環境へ導入する。KonomiTV相当の2060で対応表と索引を読み、一件の個別PNGを取得する。同じサービスをKomorebi相当のHTTPから取得し、KonomiTVのロゴAPIがHTTP 200になることを確認する。

記録するのはサービス件数、対応ロゴ件数、PNG byte数、同じ内容であることを示すSHA-256、HTTP状態、所要時間だけとする。放送ID、サービス名、画像、接続先、生の要求・応答は保存しない。提供元にロゴがない場合は安全な後退だけを確認し、実PNGを未実施として明記する。

## ライセンスとデータ

外部コードと実ロゴを製品、fixture、releaseへコピーしない。実行時に取得した画像は一要求の処理中だけ保持し、SazanamiのDB、ファイル、通常ログへ保存しない。利用者が設定したMirakurunとクライアント間の中継であり、公開配布物に放送局ロゴを含めない。
