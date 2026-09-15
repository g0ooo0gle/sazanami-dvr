# Mirakurunチャンネル初期化仕様 v1

- Artifact status: Accepted
- Requirement prefix: `MCB`
- Authority: 2026-09-15のプロジェクトオーナー指示
- Related ADR: [ADR-0073](../../docs/adr/0073-mirakurun-channel-bootstrap.md)
- Related runtime format: [CtrlCmdチャンネル設定 v1](channel-map-v1.md)
- Product base: `2b55d9703f7a2644cde731fa3eddfb230d06632c`

## Purpose

MirakurunまたはmirakcのURL一つから、KonomiTV向けの静的`channels.json`を初回生成する。APIにないTSIDは
短時間のservice streamから検証して取得し、既存fileや通常運転中のsnapshotを暗黙に変更しない。

## Requirements

### MCB-001: 公開入口を一つにする

公開CLIは次の形とする。

```text
sazanami-dvr setup --mirakurun-url <http-or-https-url> [--data-root <absolute-directory>]
```

`--mirakurun-url`は必須とし、既存Mirakurun adapterと同じURL検証を使う。`--data-root`の既定値は
`/var/lib/sazanami-dvr`、出力先は常に`<data-root>/channels.json`とする。Provider名、出力path、TSID、
補助flagを別引数にしない。余分な位置引数と未知のflagは終了code 2で拒否する。

### MCB-002: DB状態を先に固定する

全体操作は30分以内で終了する。Setup専用のSQLite入口がdata rootのowner lockを一度だけ取得し、DB状態確認、
EMPTYの初期化、Store利用、map公開が終わるまで同じlockを保持する。同じdata rootを使う録画サービスや
別保守操作と並行しない。

- `EMPTY`: embedded migrationでCURRENTまで初期化する。既存DBがないためbackupは作らない。
- `CURRENT`: schemaを変更せず続ける。
- その他: DB、map、providerを変更せず`current-database-required`相当の固定理由で終了する。

BEHINDを自動migrationしない。利用者は`db status`を確認し、必要な場合だけ従来の`db migrate`を実行する。

### MCB-003: 既存と同じcatalog同期を完了する

起動時recoveryと自動GCを実行した後、Mirakurunの`/api/version`、`/api/services`、`/api/programs`を
既存の上限、期限、逐次HTTP契約で取得する。Backend IDは正規化URLのhashから既存規則で作る。
同期が完了しない世代をmap検証へ使わない。GC失敗だけでは同期を止めず、親contextが無効ならproviderへ接続しない。

### MCB-004: bootstrap用service情報を上限付きで読む

Catalog同期後に`/api/services`をもう一度取得し、最大4,096件まで逐次decodeする。一件は次を必須とする。

- `id`: 正のsigned 64 bit範囲で、`networkId * 100000 + serviceId`と一致
- `networkId`、`serviceId`: unsigned 16 bit
- `name`: 有効なUTF-8、空でない、4,096 bytes以下
- `type`: unsigned 16 bit
- `remoteControlKeyId`: 欠落またはnull、またはunsigned 8 bit

未知fieldは既存の深さ・token上限で読み飛ばす。Duplicate key、不正JSON、上限超過、locator重複は全体を失敗させる。

### MCB-005: KonomiTV固定版が使う2K serviceだけを選ぶ

選択対象はservice type `0x01`、`0x02`、`0xA1`、`0xA2`とする。`0xAD`はBS4Kが製品範囲外のため選ばない。
その他のtypeも自動mapへ含めない。選択結果が0件または4,096件を超える場合は失敗する。

Probe順はnetwork ID、service ID、provider locatorの昇順に固定する。

### MCB-006: 一serviceずつPATを取得する

各serviceに次のHTTP GETを一件ずつ行う。

```text
/api/services/{canonical-id}/stream?decode=0
```

- `Accept: video/MP2T`
- `X-Mirakurun-Priority: 0`
- redirect、圧縮response、200以外、MPEG-TS以外のContent-Typeを拒否
- 同時接続数1
- 接続・header期限10秒、read idle期限10秒、service全体期限15秒
- 一serviceの読出し上限1 MiB、一回のbufferは32 KiB以下

PAT取得後は直ちにbodyをcloseし、接続slotを解放する。録画、ライブ視聴、再接続として数えず、自動retryしない。

### MCB-007: PATとservice identityを照合する

任意のHTTP read境界を既存`Packetizer`で188-byte packetへ戻す。PID 0だけを`PSICollector`へ渡し、
`ParsePAT`でsection length、current-next、section番号、CRC、media program数、PMT PIDを検証する。

Media programは一件で、program numberが対象serviceのSIDと一致しなければならない。このPATの
`TransportStreamID`だけを採用する。PAT取得前のEOF、timeout、取消し、1 MiB到達、同期不良、不正PSI、
複数program、SID不一致は全体を失敗させる。

### MCB-008: channel-map-v1を決定的に生成する

Top-levelは既存形式とbackend IDを使う。各serviceは次の値を使う。

| Field | Value |
|---|---|
| `provider_locator` | APIの`id`をcanonicalな10進数文字列にした値 |
| `network_id` | APIの`networkId` |
| `service_id` | APIの`serviceId` |
| `transport_stream_id` | MCB-007で確認したPATのTSID |
| `provider_name` | 空文字列 |
| `network_name` | 空文字列 |
| `transport_stream_name` | 空文字列 |
| `remote_control_key_id` | API値。欠落またはnullなら0 |
| `partial_reception` | `false` |
| `epg_capture` | `true` |
| `search` | `true` |

Serviceはnetwork ID、TSID、service ID、provider locator順に並べる。UTF-8 JSONを2-space indent、LF、
末尾LF一つで生成し、1 MiB以下にする。Unknown field、暗黙default、時刻、URL、生成host固有値を含めない。

### MCB-009: 候補を既存runtimeで全件検証する

候補はdata root内の0600の通常fileへ書き、内容を同期する。公開前に既存channel-map-v1 decoderと、最新の
COMPLETED catalog generationを使うsnapshot builderへ渡す。Backend、locator、ONID、SID、service type、名称、
重複、件数、file上限を既存規則で検証する。候補の全serviceがsnapshotに含まれない場合は公開しない。

### MCB-010: 既存fileを上書きしない

`<data-root>/channels.json`が存在しなければ、同じfilesystem上の候補を上書きなしの原子的操作で公開し、
data root directoryを同期する。公開処理と競合してfileが現れた場合も上書きしない。

既存の通常fileが候補とbyte-identicalなら候補を削除し、`unchanged`の成功とする。異なる通常file、symlink、
directory、device、socketがある場合は候補を削除し、既存objectを変更しない。失敗時に既存mapのbackup、merge、
repair、削除を行わない。

### MCB-011: 結果を固定形式で出力する

成功は終了code 0とし、`result=completed`、service件数、`created`または`unchanged`だけを出力する。
引数以外の失敗は終了code 1とし、安定した理由だけをstderrへ出す。

通常出力とerrorへ、URL、path、service／番組名、provider response body、生のHTTP／OS／SQLite error、
JSON内容を含めない。TS byteをfile、DB、logへ保存しない。

### MCB-012: 通常運転から分離する

`recording serve`、`ctrlcmd serve`、定期catalog更新、CtrlCmd要求は本bootstrapを呼ばない。生成mapを起動時に
一度読み、既存の変更不可snapshotとして使う。Map変更はservice停止、利用者による既存fileの確認、setup、
validate、再起動の明示手順で行う。

### MCB-013: KonomiTVの標準例はライブを直結する

同梱KonomiTV設定例と公開導入文書は次を標準とする。

```yaml
always_receive_tv_from_mirakurun: true
```

この場合、ライブ視聴だけをMirakurun / mirakcへ直接接続し、番組表と予約はSazanamiのCtrlCmdへ接続する。
`false`はSazanamiの上限付きライブ中継を明示的に選ぶ設定として説明し、既存中継の実装とtestを維持する。

## Verification matrix

| Layer | Cases |
|---|---|
| CLI | 必須URL、既定data root、任意data root、未知flag、位置引数、30分取消し |
| DB | EMPTY初期化、CURRENT、BEHIND、FUTURE、DRIFTED、owner lock競合 |
| Catalog | version取得可否、services/programs成功、同期失敗、recovery、GC失敗継続 |
| Service decode | 4対象type、除外type、remocon値／欠落／null、重複、0／4,096／4,097件 |
| PAT probe | query/header、任意chunk、garbage、複数packet、CRC、program数、SID、EOF、timeout、1 MiB |
| Publish | 決定的JSON、0600、検証失敗、作成、同一byte、異なるfile、symlink、競合、directory sync |
| Regression | catalog sync、ctrlcmd validate／serve、recording serve、1060／1021、全Go test、race、vet、build |
| Packaging | Linux／ComposeのURL-only synthetic lifecycle、KonomiTV設定例true、文書link |
| Hardware | 実Mirakurun、空きチューナー、実放送PAT、KonomiTV画面。未実施なら`NOT RUN` |

時間指定の長時間耐久試験は完了条件に含めない。
