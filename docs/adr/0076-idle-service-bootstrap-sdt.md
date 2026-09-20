# ADR-0076: 初期設定のサービス確認をSDT actualで補完する

- Status: Accepted
- Proposed date: 2026-09-20
- Decision date: 2026-09-20
- Decision owner: Project owner
- Authorization: 本書のSDT補完、通常の録画・視聴・DBを変えない範囲、修正・テスト・公開の進め方を提示し、利用者が「いいよ」と明示承認した。
- Product base: `d54d8e1c26976f9344eeb0976c22900d461013eb`（v1.3.1）
- Related planning records: `sazanami-planning`のPlan 0097、Handoff 0074
- Partially supersedes: [ADR-0073](0073-mirakurun-channel-bootstrap.md)、[MCB-004・006〜008](../../spec/ctrlcmd/channel-bootstrap-v1.md)

## 目的と根拠

初回のチャンネル設定が、一部サービスからPATを受け取れないために止まる問題を扱う。
通常の録画・視聴を変えず、放送内の識別情報で確認できるサービスは設定に含めたい。

Mirakurun 4.1.3のサービス単位のstreamは、PMT受信までPATを含む出力を待つ経路を持つ。
物理チャンネル単位のstreamにはこの待機条件がない。実環境でも個別streamは0 byte、
物理streamはデータを返したとの報告があるが、当該放送のPMTやSDTは未確認である。

SDT actualには現在のTSに属するサービスのONID、TSID、SIDが記載される。
そのため、PATを複数program対応へ広げず、初期設定専用のSDT確認を一つ加える。
PMTの到着や映像・音声の有無は、所属確認の条件にしない。

## 判断

ISB-001〜005を採用する。製品側ではHandoff 0074の文書copyを読み戻してから実装する。

### ISB-001: 通常経路を維持し、取得不能だけを補完する

まず既存のservice PAT probeを一回行う。成功時はその結果を使い、追加接続しない。
親contextが有効で、次の許可表に一致する場合だけ、元の接続を閉じた後にSDT probeを一回行う。
公開CLIの`channel-pat-probe-failed`へまとめる前のProvider Failureを判定する。

| Reason | Diagnostic | 扱い |
|---|---|---|
| `TIMEOUT` | `http-timeout` | 補完する。既存分類では接続・TLS・headerのtimeoutを含む |
| `TIMEOUT` | `probe-read-timeout` | 補完する |
| `TIMEOUT` | `context-deadline` | service probeの15秒期限だけ補完する。親context終了時は接続しない |
| `EARLY_EOF` | `probe-pat-not-found` | 補完する |
| `OVER_LIMIT` | `probe-byte-limit` | 補完する |
| その他 | 上記以外の全組合せ | 補完せず終了する |

取消し、認証・HTTP status、redirect、圧縮、Content-Type、CRC、SID不一致、不正packet/PSI、
過大なContent-Length、その他の内部・通信エラーでは補完しない。失敗分類と診断値を明示的に照合し、
`UNAVAILABLE`や`OVER_LIMIT`全体をまとめて許可しない。`probe-zero-progress`、
`probe-read-deadline-failed`、`probe-read-failed`、`probe-content-length-over-limit`も補完しない。
親の30分期限は判定前と追加接続前に確認し、延長しない。

Mirakurunのservice routeはheaderを設定しても明示flushしないため、PMT待機がheader timeoutとして
現れる可能性がある。HTTP 200受信後のtimeoutだけに限定すると、この経路を補完できない。

### ISB-002: 同じproviderの検証済みchannelだけを開く

bootstrapのサービス取得時だけ、任意の`channel.type`と`channel.channel`を保持する。
補完で使うtypeは`GR`・`BS`・`CS`、channelはASCII英数字・`_`・`-`の1〜64 bytesとする。
欠落・null・型違い・対象外・形式不正な値では、そのサービスの補完だけを不可とする。
正常なservice PAT経路は引き続き使える。JSON自体の構文・重複キー・深さ・token・読出し上限の違反は
従来どおり全体を失敗させる。任意fieldの意味上の不正と、JSON構造の不正を分ける。

接続先は設定済みproviderのbase path配下の
`/api/channels/{type}/{channel}/stream?decode=0`に固定する。
APIからURLやhostを受け取らず、channelを別のIDから組み立てない。
priority 0、同時1接続、HTTP応答の検証はservice probeと同じにする。
追加endpointが利用できなければ全体を失敗させ、別providerや別channelは探さない。

初版はserviceごとに独立して確認する。同じchannelを複数serviceが共有していても、SDT結果を
cacheして使い回さず、各serviceの補完時に一回だけ取得する。失敗すればその時点でsetup全体を終了する。
最大4,096 service、各serviceはPAT一回と必要時のSDT一回だけで、setup全体30分の上限を共有する。
同じchannelの再取得により期限へ達した場合も部分mapは公開しない。多数の休止serviceで実際に
この上限が支障になった場合に、同一setup内の結果共有を別途検討する。

### ISB-003: SDT actualの全sectionから対象サービスを確認する

既存PacketizerとPIDごとのPSICollectorを使い、PID `0x0011`のSDT actual（table ID `0x42`）を確認する。
SDT otherやBATは所属の証拠に使わない。SDTはCRC、長さ、current-next、section番号、
可変長部分の境界を検証する。同じONID・TSID・version・last section番号の全sectionを揃え、
SDTの`original_network_id`がAPIの`networkId`と一致し、service loopの`service_id`に対象`serviceId`が
一回だけ含まれる場合に、SDTの`transport_stream_id`を採用する。

sectionは最大1,024 bytes、最大256 section、サービスは合計4,096件までとする。
同じsectionの同一内容の再送は許し、件数へ重複加算しない。最初の有効なcurrent SDTで収集世代を固定し、
完了前のversion切替、同じsectionの内容変更、ONID/TSID/last section番号の変化、重複SIDは即失敗する。
収集のリセットや自動再試行はしない。`current_next=0`の次期情報は世代を切り替える根拠に使わず無視する。
section 0からlastまでが揃った時点で対象所属を検証し、一致すれば直ちに成功して接続を閉じる。
全sectionに対象SIDがなければ直ちに失敗する。完了後のpacketを待って追加検証しない。
sectionが揃わないまま期限・読出し上限へ達した場合も失敗する。

running statusはサービスを除外する条件にしない。所属を確認したことと、今すぐ録画できることは区別する。
記述子の文字列解読、番組表の解析、複数program PAT parserは追加しない。

### ISB-004: 追加処理の上限を固定する

SDT probeは15秒または64 MiB読出しで終了する。接続・header・read idleは各10秒、read bufferは32 KiB以下。
service probeと合わせて一件最大30秒・65 MiB、setup全体30分と逐次接続は維持する。
保持するSDT section本体はheader・CRCを含めて最大256 KiB（1,024 bytes×256 section）、
section長・受信済み印・SID重複確認などの管理データは別枠で最大64 KiB、計320 KiBまでとする。
これはSDT解析で保持するデータの上限であり、既存Packetizer/PSICollector、32 KiBのread buffer、
HTTP clientやGo runtimeのメモリを含むプロセス全体の使用量ではない。
TS全体をメモリ・DB・fileへ保存しない。
成功・失敗・取消しのすべてでbodyと接続slotを解放し、再試行や期限の再設定による延長はしない。

64 MiBは物理streamに映像・音声が混在することを考慮した製品側の上限であり、
全放送条件で受信できる保証ではない。高速な不要packetを読み流した後にSDTが届く合成ケースで確認する。

### ISB-005: 設定の保存と通常運転は変えない

既存PATまたは上記SDTで全対象サービスを確認できた場合だけ、従来のchannel-map-v1を生成する。
候補の全件validate、原子的公開、既存map保全、同一内容での再実行、CLI出力の秘匿を維持する。
確認できないサービスの自動除外やTSIDの仮置きはしない。MCB-011の出力形式も変えない。

DB schema、設定形式、通常の録画・HLS・ライブ通信、共有`mpegts.ParsePAT`は変更しない。
定期同期や起動中のmapへSDT probeを追加しない。

## 検証と公開判断

合成HTTP/TSで、既存経路の成功、service timeout/EOFからの補完、PAT/PMTがなくSDTだけで所属を確認できる場合、
補完禁止のエラー、異なるONID/SID、SDT other、CRC・section・版の不正、上限・取消し・接続解放を確認する。
従来のsetup、map保全、録画、HLSの回帰テストと通常CIを通す。実施条件はHandoff 0074にまとめる。

実環境の対象SIDがSDT actualにあるかは`NOT RUN`。合成テストの成功で実障害の解消を宣言しない。
実機結果が必要な場合は、検証済み手順をメンテナンスタスクへ渡して別途確認する。
実装・検証を終えた小修正は、利用者の既存指示に従い版更新と公開へ進める。本番切替は含まない。

## 出典と検討した案

- Mirakurun `4.1.3` / `5770073e9b30d523512858ca82f45386f51a08fd`:
  [Tuner.ts](https://github.com/Chinachu/Mirakurun/blob/5770073e9b30d523512858ca82f45386f51a08fd/src/Mirakurun/Tuner.ts)、
  [TSFilter.ts](https://github.com/Chinachu/Mirakurun/blob/5770073e9b30d523512858ca82f45386f51a08fd/src/Mirakurun/TSFilter.ts)。
  service/channel指定とPMT待機の違いはHandoff 0074に記録。外部コードの複製・実行なし。
  [service stream route](https://github.com/Chinachu/Mirakurun/blob/5770073e9b30d523512858ca82f45386f51a08fd/src/Mirakurun/api/services/%7Bid%7D/stream.ts#L67-L85)
  ではheaderの明示flushがないことも確認した。
- [ARIB STD-B10 v5.13-E1](https://www.arib.or.jp/english/html/overview/doc/6-STD-B10v5_13-E1.pdf):
  Part 2 §5.2.6、冊子95〜98頁を2026-09-20に読み取り確認。識別field、section、CRCの意味を参照。
  仕様本文・表・画像は転載せず、実装とfixtureは独立して作る。
- 手動部分map: 現行の正式な選択肢だが、検証済みTSIDが必要なので今回のURL-only導入の解決にはならない。
- raw PATのみの補完: 対象SIDがPATから消える休止状態を扱えないため、今回の推奨案にはしない。
- 自動skip: 設定からサービスが欠落するため推奨しない。UPS-004の境界を維持する。

本判断は、ARIB規格の全体対応やmirakcでの実機確認を意味しない。
