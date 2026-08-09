# ADR-0040: 局ロゴは保存せずMirakurunから上限付きで中継する

- Status: Accepted
- Date: 2026-08-10
- Deciders: Project owner, Codex（v1.0までの委任範囲）
- Related plan: [`../plans/0053-bounded-service-logo-relay.md`](../plans/0053-bounded-service-logo-relay.md)
- Supersedes: None
- Superseded by: None

## 背景

KonomiTV v0.14.1は、EDCB接続時に2060でロゴ対応表、ディレクトリ索引、選んだ画像を取得する。Komorebi 1.1.0-beta6のEDCB直接接続は、HTTPの`/legacy/logo.lua`から同じ局ロゴを取得する。Sazanami DVR v0.0.17は両方とも未対応である。

Mirakurun 4.1.3はサービスごとのPNG取得APIを持つ。KonomiTVとKomorebiはロゴを取得できない場合に標準画像へ戻り、取得できた画像はクライアント側でキャッシュする。Sazanamiが全ロゴを永続化しなくても、通常の表示を改善できる。

## 決定

完成済み番組表で選択されたサービスだけを対象に、Mirakurunの`GET /api/services/{id}/logo`を上限付きで中継する。SazanamiはロゴをDBやファイルへ保存しない。

KonomiTVへは、現在のスナップショットから`LogoData.ini`相当の対応表と`LogoData\\*.*`相当の索引を生成する。ロゴIDは、同じネットワーク内で重複せず、サービスIDが0～4,095に収まる場合だけサービスIDを使う。KonomiTVが索引にある固定名を選んだ場合に限り、対応するMirakurunサービスのPNGを取得して2060で返す。

Komorebiへは、`GET`または`HEAD /legacy/logo.lua?onid=...&sid=...`を提供する。完成済みスナップショットで一件だけ照合できたサービスのPNGを返す。要求IDが不正、未知、重複の場合は提供元へ接続しない。

ロゴ専用HTTP adapterは一件2 MiB、応答ヘッダー64 KiB、取得5秒、同時4件を上限とする。proxy環境変数、redirect、圧縮、TLS検証無効を使わない。200、`image/png`、上限内の本文だけを成功とする。ロゴがない場合と取得失敗は、その表示だけをクライアントの標準画像へ戻し、番組表、予約、録画、ライブ処理を止めない。

既存の`Bitrate.ini`と`EpgTimerSrv.ini`は固定値のままとし、これらの要求からDBやネットワークへ接続しない。任意path、任意URL、ロゴ以外のファイル取得には広げない。

## 理由

ロゴをその場で中継すれば、既存カタログのサービス照合とクライアントのキャッシュを再利用できる。DB形式、保存容量、更新世代、掃除、画像処理を増やさず、KonomiTVとKomorebiの二つの未対応経路を一つの提供元で満たせる。

完成済みスナップショットから固定名を生成すれば、利用者入力をMirakurunのpathへ直接使わずに済む。範囲外や重複を一覧へ載せないことで、別局のロゴを返すより標準画像へ倒せる。

## 影響

- `recording serve`は、ロゴ表示要求を受けたときだけMirakurunへ短いHTTP接続を行う。
- CtrlCmd 2060は、固定INIの一件要求に加えて、固定したロゴ2件要求と個別PNG一件を扱う。
- 録画用HTTPにKomorebi向けの読み取りpathが一つ増える。
- DB migration、常駐キャッシュ、画像ライブラリ、新しい依存は増えない。
- 提供元にロゴがなければ、従来どおりクライアントの標準画像になる。

## 採用しなかった案

### 全ロゴをcatalog syncで保存する

表示は速くなるが、永続形式、更新、掃除、容量、著作物の保持方針が増える。対象クライアント側にキャッシュがあるため採用しない。

### 共通の代替PNGをSazanamiへ埋め込む

両クライアントが既に標準画像を持つ。同じ機能を重ねても局を識別できないため採用しない。

### 2060の任意ファイルを許可する

ロゴ対応に不要で、path traversalやホスト情報公開の範囲を増やすため採用しない。
