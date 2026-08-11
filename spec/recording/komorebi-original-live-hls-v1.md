# Komorebi向け原画質ライブHLS仕様 v1

- Status: Accepted
- Date: 2026-08-11
- Requirements: `KHL-001`～`KHL-020`
- Related decision: ADR-0057
- Target client: Komorebi `1.1.0-beta6` / `41916c63cdf53cc62643333f630bb1095c14a298`
- Design inspection base: `6443e5e6b9dba5f207a5f913ecc307f5819670c8`
- Target product base: `5deb65c9f0604d1a089a85d32123e27de7f72e28`
- Media contract: RFC 8216 MPEG-2 Transport Stream Media Segments

## 目的

KomorebiのEDCB直接接続で直接ライブを無効にした場合に、主画面と二画面目へ原画質のMPEG-TSを
HLSとして配信する。HTTP 2xxだけの仮応答を禁止し、固定クライアントが実際にplaylistとsegmentを
取得して再生できることを完了条件にする。

本仕様で「原画質」とは、providerから受け取ったTS packetの内容と順序を変えず、HLSとして再生可能な
境界へ分けることを指す。解像度、bitrate、frame rate、codec、音声、字幕の変換は含まない。

## 対象となる呼び出し

対象は固定したKomorebiの次の呼び出しに限る。

| 呼び出し | 固定ソースのpathとsymbol |
|---|---|
| resolverから`ctok.view`と`ctok.xcode`を読む | `app/src/main/java/com/beeregg2001/komorebi/data/repository/edcb/EdcbLiveRepository.kt::fetchResolverCtok` |
| TvCast、POST view、4秒待機、GET view URLを作る | 同ファイルの`getLiveStreamUrl` |
| 主画面`n=0`、二画面目`n=1`を開始する | `app/src/main/java/com/beeregg2001/komorebi/viewmodel/LivePlayerViewModel.kt::playMainChannel / playDualChannel` |
| EDCB非直接経路をHLSとして再生する | 同ファイルの`startPlayback` |

固定コミットの`LICENSE`はMITである。確認はsourceの読み取りだけで、外部コード、fixture、通信記録を
製品へコピーしない。

## 要件

### KHL-001: resolverの原画質optionを共用する

引数なしの`GET /komorebi/resolver.lua`は、原画質xcode配信仕様v1のKXP-001と同じ空tokenと
`option=10`一件を返す。録画とライブで別の画質一覧を作らない。option 10は「オリジナル（変換なし）」を
意味し、transcode済みを意味しない。

### KHL-002: TvCastの要求を厳密に受ける

`GET /api/TvCast`は次のqueryを各一件、大小文字を区別して受ける。

| 名前 | 受理する値 |
|---|---|
| `id` | `{onid}-{tsid}-{sid}`。各値は先頭0のない10進数1～65535 |
| `n` | 主画面`0`または二画面目`1` |
| `json` | `1` |
| `ctok` | 空文字 |

未知、欠落、重複、空、不正区切り、符号、先頭0、overflow、trailing文字を400 `invalid-query`とする。
GET以外は405と`Allow: GET`を返す。成功は200、`application/json; charset=utf-8`、固定した小さい
`{"result":true}`を返す。放送IDや内部IDを本文へ返さない。

### KHL-003: TvCastではprovider streamを開かない

TvCastは要求開始時の一つの完成済みチャンネルsnapshotでONID、TSID、SIDを一意に照合し、HLS専用の
live managerへ選択を保存する。0件、複数件、未検証service、不正locatorは404
`service-unavailable`とする。

同じ`n`の古い選択またはsessionがあれば、先に取消して置き換える。TvCast成功時点ではproviderへ
接続せず、30秒以内に対応するPOST viewが来なければ選択を解放する。

### KHL-004: POST viewのquery、form、Cookieを固定する

`POST /api/view`は、KHL-002と同じ`id`と`n`に加え、次のqueryを各一件受ける。

| 名前 | 受理する値 |
|---|---|
| `option` | `10` |
| `hls` | 1～96 byteのASCII英数字、`_`、`-`。先頭は英数字 |
| `ctok` | 空文字 |

bodyは`Content-Type: application/x-www-form-urlencoded`で、decode後の長さを1 KiB以下とする。
fieldは`ctok=`と`open=1`を各一件だけ受ける。`Cookie`は一つの`ctok=`を含み、同名Cookieの重複、
非空値、未知form field、trailing bodyを拒否する。

正常にworkerを開始または同じsessionを確認した場合は204で本文を返さない。query、body、Cookieの
不一致は400 `invalid-view-request`、対応するTvCast選択がない場合は409 `selection-required`とする。

### KHL-005: sessionの同一性と置換を固定する

sessionは`n`、照合済み放送、`hls` key、option 10の組で識別する。同じ組へのPOST再送はprovider streamを
二重に開かず、同じsessionの状態を返す。異なるkeyまたは放送へのTvCast／POSTは古いsessionをcancelし、
worker、Provider lease、fileを閉じてから置き換える。

同じkeyを別slotまたは終了後の別sessionへ再利用しない。process内で主画面と二画面を最大一件ずつ持ち、
一方の置換、失敗、停止が他方を止めない。

使用済みkeyはprocess内のtombstoneへ最大4,096件まで記録する。上限到達後も既存sessionとretention中の
playlist／segmentは読み出せる。既存sessionの同一要求ではない新しいkeyを受けた場合は、providerを開かず
503 `session-key-limit`とする。keyそのものをHTTP error、通常出力、診断情報へ出さない。

### KHL-006: workerはservice contextで開始する

POST viewは、録画常駐processのservice contextから最大12時間のworker contextを作る。HTTP request contextを
workerの親にしない。POST応答の完了またはclient切断だけでstreamを止めない。

HLS専用`liverelay.Manager`の`Open`でprovider streamを一度だけ開き、返された`Stream.Copy`をTS segmenterへ
接続する。開始失敗時は204を返さず、503 `live-provider-unavailable`とし、部分的なresourceを閉じる。

### KHL-007: GET viewはMedia Playlistを返す

`GET /api/view`はPOSTと同じqueryを各一件受け、bodyを受けない。`Cookie: ctok=`を必須とする。対応する
sessionとkeyが存在しない場合は404、終了後のretentionを過ぎた場合は410を返す。

完成segmentが3件になるまで最大2秒待てる。準備できなければ503 `playlist-unavailable`とし、空または
壊れたplaylistを返さない。成功時は200、`Content-Type: application/vnd.apple.mpegurl`、UTF-8、BOMなし、
`Cache-Control: no-store`でMedia Playlistを返す。HEAD、POST以外のmethodは対象に加えない。

### KHL-008: segment URLと応答を固定する

playlistは`/komorebi/live/{hls}/{sequence}.ts`形式の相対URLを記載する。`hls`は既存sessionと完全一致し、
`sequence`は先頭0のない非負64 bit整数とする。GETだけを受け、`Cookie: ctok=`を必須とする。query、
未知path、別slotのkey、未完成fileを受けない。

完成済みの通常fileだけを返す。Rangeなしは200、`Content-Type: video/mp2t`、正確な`Content-Length`、
`Accept-Ranges: bytes`とする。満たせる単一Rangeは`http.ServeContent`で206、正確な`Content-Range`と
対象部分を返す。複数Rangeだけはmultipartへ広げず416とする。不正または満たせない単一Rangeの扱いは
`http.ServeContent`の標準動作に従う。retention中は同じsequenceから同じbyte列を返し、その後は410とする。

### KHL-009: 入力chunkを188 byte packetへ組み直す

providerのchunk境界が188 byteに一致することを前提にしない。最大192,512 byteの既存chunkを受け、
packet未満の末尾だけを次のWriteまで保持する。TS全体、番組全体、無制限queueをmemoryへ置かない。

開始時は最大64 KiB内で、188 byte間隔に5個以上の同期byte`0x47`が並ぶ位置だけを採用する。同期確立後の
欠落、追加byte、transport errorで同期が崩れた場合はsessionを終了し、無制限resyncやbyte破棄をしない。
終了時に188 byte未満が残れば未完成segmentとともに破棄する。

### KHL-010: PATとPMTを完全に検証する

PID 0のPATからprogram 0を除くmedia programを一件だけ取得し、対応するPMT PIDを得る。PMTからPCR PIDと
video elementary PIDを得る。PSIはpayload unit start、pointer field、複数packetに分かれたsection、
section length、table ID、current-next、version、section番号、MPEG-2 CRC32、continuity counterを検証する。

一sectionは4 KiB、同時に組み立てるPSIはPATと選択PMTの二つまでとする。versionは5 bitの循環値として
扱い、31から0への更新を正常に受理する。数値の大小だけで巻戻りとは判定しない。同じversionで内容が
変わる場合、CRC不一致、欠落、重複、truncated、不正pointer、上限超過ではtableを採用しない。
PATとPMTは別のtableとして現在採用中のversionを個別に保持し、両者のversion番号の一致を求めない。

### KHL-011: 単一programと対応videoを必須にする

入力PATにmedia programが0件または複数件ある場合は失敗する。PMTでPCR PIDがない、またはvideo PIDが
0件・複数件の場合も失敗する。初期対応のvideo stream typeはMPEG-2 Video `0x02`とH.264/AVC `0x1b`に
限る。他のstream type、BS4K、HEVC、multi-programを推測で対応済みにしない。

audio、caption、data放送など選択program内の他PIDはpacketを変更せずsegmentへ残す。保持したことだけで
固定Androidクライアントが表示・再生できるとは表明しない。

### KHL-012: random accessを確認してから境界にする

新しいsegmentの開始候補は、それぞれ現在採用中のversionのPATとPMTを含み、その後のvideo PIDで
`random_access_indicator=1`とPES payload unit startを順に確認できるpacket列とする。segmentは候補PATから
始め、random access前に対応PMTを含める。元packetの順序、PID、continuity counter、timestamp、payloadを
変更、複製、削除しない。

現在の有効境界と次の有効候補で得た境界PCRの差が1秒以上になった後、その候補で切る。PCR差が2秒を
超えるか、次の境界までに32 MiBへ達した場合は、任意位置で切らずsessionを
`random-access-unavailable`で終了する。完成segmentは188 byteの倍数で、一program、PAT、PMT、
random accessを再解析してから公開する。

### KHL-013: durationと不連続をPCRから決める

有効なsegment境界ごとに、境界以後の最初の27 MHz PCRを境界PCRとして保持する。segment durationは
現在の境界PCRと次の有効境界PCRの差で求める。PCR baseの33 bit wrapとextensionを扱い、wall clock、
受信byte数、平均bitrate、segment内の最後のPCRから`EXTINF`を作らない。

PCR欠落、逆行、不正extension、2秒超過、0以下のdurationではsegmentを公開しない。次の有効境界がない
末尾はdurationを確定できないため、未完成segmentとして破棄する。v1ではPTSを解析しない。
PAT／PMTの採用中version、trackの種類・PID、PCR PIDまたはPCR列が変わる場合は、次のsegment前へ
`EXT-X-DISCONTINUITY`を付ける。対応できない変化ではsessionを終了する。

### KHL-014: Media PlaylistをRFC 8216へ合わせる

playlistは少なくとも次を順に含む。

- `#EXTM3U`
- `#EXT-X-VERSION:3`
- `#EXT-X-TARGETDURATION:2`
- 必要な`#EXT-X-DISCONTINUITY-SEQUENCE`
- `#EXT-X-MEDIA-SEQUENCE:{先頭sequence}`
- 各segmentの小数`#EXTINF:{PCR duration},`と相対URL
- 不連続直前の`#EXT-X-DISCONTINUITY`
- terminal時だけ末尾の`#EXT-X-ENDLIST`

`TARGETDURATION`はsession中に変えない。`EXT-X-PLAYLIST-TYPE`、Master Playlist、variant、encryption、
byte rangeを加えない。sequenceは0から単調増加し、減少、wrap、再利用しない。playlistは完成segmentを
追加したときだけ更新し、同じsequenceのURLとbyte列を変更しない。

### KHL-015: windowとretentionを守る

live playlistは新しい順に最大60秒分を持ち、terminalでない間は合計6秒以上を残す。古いsegmentは
sequence順にだけplaylistから外す。

外したsegmentは、そのsegment durationと、そのsegmentを含んだ最長playlist durationの合計時間まで
取得可能にする。session終了時も、最後に配布したplaylist duration以上はplaylistとsegmentを保持する。
retention満了前に容量を空ける必要が生じた場合は、新しいsegmentの生成を止め、古いfileを早く消さない。

### KHL-016: cacheの場所と容量を限定する

segmentはdata root直下のSazanami専用HLS cache directoryへ置く。directoryは0700、fileは0600の通常fileとし、
symlink、hard link、既存file、owner違いを受けない。部分fileを同directoryで作り、close後に完成名へ
原子的に公開する。録画root、DB、通常ログ、URLへhost pathを出さない。

上限はsegment一件32 MiB、session一件512 MiB、二session合計1 GiBとする。合計1 GiBには、稼働中の
session、作成途中のfile、終了後にretentionしている全世代のfileを含める。作成前と書込み中に上限を確認し、
算術overflowを拒否する。上限超過ではsessionを`hls-cache-limit`で終了する。

起動時は固定cache directory内のSazanami形式の過去sessionだけを削除する。他directoryを走査せず、
symlinkをたどらない。停止時も録画fileやDBを削除しない。

### KHL-017: 終了と再試行を決定的にする

provider終了・失敗・stall、TS不正、file失敗、cache上限、置換、最後のplaylist／segment accessから30秒、
開始から12時間、service context取消し、process停止でsessionを終了する。自動retryは行わない。

終了時は、次の有効境界がなくdurationを確定できない末尾を含む未完成segmentをplaylistへ加えず削除する。
完成segmentが一件以上あれば最後のplaylistへ
`EXT-X-ENDLIST`を付ける。Provider lease、stream、file、timer、workerを一度だけ閉じる。retention後に
cache fileとsession情報を削除し、同じslotで新しいTvCastを受けられる。

固定クライアントの再試行は新しいTvCast、key、POST viewとして扱う。終了済みworkerへappendしない。

### KHL-018: 録画と既存ライブの資源を壊さない

HLSは主画面と二画面の最大二sessionとし、待ちqueueを作らない。3件目に相当する不正slotを受けない。
CtrlCmd直接ライブとHLSは既存のライブ用provider adapterの4接続枠を共有し、合計上限を越えた開始は
503にする。録画用provider adapterと録画同時数は消費しない。

TvCast選択、POST開始、session置換、idle、shutdownが競合しても、同じprovider streamを二重に開いたり、
別sessionのleaseを閉じたりしない。race detectorでsession表とretentionを確認する。

### KHL-019: 失敗理由と出力を固定する

主な固定理由は`invalid-query`、`service-unavailable`、`invalid-view-request`、`selection-required`、
`live-provider-unavailable`、`playlist-unavailable`、`ts-sync-unavailable`、`psi-invalid`、
`single-program-required`、`video-unsupported`、`random-access-unavailable`、`timestamp-invalid`、
`hls-cache-limit`、`session-key-limit`、`session-ended`とする。内部error本文をHTTPへ返さない。

通常出力へ接続先、接続元、放送ID、service、HLS key、token、segment sequence、番組、file名、host path、
TS内容、生の要求・応答を出さない。起動・終了件数、転送byte数、所要時間、固定理由だけを診断に使う。

### KHL-020: 依存と対応表明を限定する

実装はGo標準libraryと既存依存だけを使う。DB migration、新しい外部依存、CGO、framework、ORM、
code generation、Node／npm、外部実行ファイルを追加しない。製品versionと変更履歴は別のrelease handoffで
更新する。

TS packet、PSI、PAT、PMT、MPEG-2 CRC32のprimitiveは、`internal/app/recording/ts_filter.go`の既存実装を
複製しない。録画filterとHLS parserから使える小さい内部packageへ必要分だけを抽出する。HLS固有の境界、
PCR、playlist処理を録画filterへ持ち込まず、汎用media frameworkにも広げない。

対応表明は「Komorebi 1.1.0-beta6、EDCB直接接続、非直接ライブ、option 10、原画質HLS、
確認済み放送種別と端末」までとする。画質変換、未知codec、BS4K、一般的なEDCB WebUI、KonomiTV HTTP API、
未知Komorebi版へ広げない。Android実機未確認の段階では`SOURCE`または`TEST`と表示し、`LIVE`にしない。

## 必須テスト

### HTTP契約

- resolverのoption 10と空tokenが録画・ライブで一致すること。
- TvCastのGET、id三要素、n 0／1、json 1、空ctok、各欠落・重複・未知query、大小文字、0・負数・
  先頭0・65535・65536・overflow、trailing、method。
- TvCastでproviderを開かず、30秒直前・到達・直後に選択を解放すること。同一slot置換と二slot独立。
- POST viewのquery、option 10、hls最小・最大・不正文字・一件超過、空ctok、content type、body 1 KiBと
  一件超過、formの重複・未知・trailing、Cookieの欠落・重複・非空。
- POST前の選択なし、放送不一致、slot不一致、同一再送、別key置換、provider開始失敗、request cancel。
- key tombstone 4,096件の境界と一件超過。上限後も既存sessionとretention中の読み出しを継続し、
  新しいkeyだけを503 `session-key-limit`とすること。
- GET viewの同一query／Cookie、準備前、2秒待機、3segment到達、未知key、終了中、retention後、method、
  playlist content type、BOMなし、no-store。
- segment GETの200、満たせる単一Rangeの206、不正・満たせない単一Range、複数Rangeの416、未知key、
  別slot、sequence負数・先頭0・overflow、query、未完成、retention、410。
- segment応答を途中で切り、Media3と同じ`Range: bytes={受信済みbyte}-`で再試行した206の内容を連結すると、
  完成segmentのbyte列と一致すること。

### TS parserとsegment

- provider chunkが1 byte、187、188、189、最大chunk、packet境界をまたぐ場合。
- 初期garbage 0、1、64 KiB、64 KiB一件超過、偽同期、5 packet未満、同期後の欠落・追加・truncated。
- PAT／PMTの単一・複数packet、pointer 0・非0、複数section、CRC正誤、version変更、31から0への循環、
  同じversionでの内容変更、PATとPMTで異なる採用中version、current-next、section番号、continuity重複・欠落、
  4 KiB一件超過。
- program 0だけ、media program 0件・1件・2件、PMTなし、PCR PIDなし、video 0件・1件・2件、
  MPEG-2 Video、H.264、HEVC、未知stream type。
- random access indicator、PES開始の前後、PAT／PMTそれぞれのversion更新、1秒直前・到達・直後、
  2秒直前・到達・直後、32 MiB一件超過、random accessなし。
- 現在と次の境界PCR、PCR base／extension、33 bit wrap、逆行、欠落、同値、duration 0、2秒超過、
  次の有効境界がない末尾の破棄。
- 完成segmentが188 byte倍数、一program、PAT、PMT、random accessを持ち、入力packetの順序とbyte列を
  変更していないこと。
- PAT／PMT、track／PID、PCR列の変更と`EXT-X-DISCONTINUITY`。PTSを解析しないこと。
- 共通内部packageへ抽出したpacket／PSI／PAT／PMT／CRC primitiveを録画filterとHLS parserの両方が使い、
  録画filterの既存testが同じ結果を保つこと。

### Playlist、session、資源

- VERSION、TARGETDURATION固定、EXTINF小数、MEDIA-SEQUENCE単調増加、URL不変、window最大60秒・最小6秒。
- playlistからの順序付き削除、segment duration＋最長playlist durationのretention、ENDLIST、
  DISCONTINUITY-SEQUENCE、sequence overflow。
- segment 32 MiB、session 512 MiB、合計1 GiBの境界と一件超過。合計へ稼働中、作成途中、retention中の
  全世代を含め、上限時にretention fileを早く消さないこと。
- cache mode、通常file、symlink、hard link、既存file、owner違い、部分公開、rename失敗、startup cleanupの
  directory境界。
- 主画面、二画面、同時二件、片方の置換・失敗・停止、CtrlCmd直接ライブとの合計4枠、録画枠との独立。
- provider stall・disconnect・terminal、file write失敗、client idle、12時間、shutdown、全競合。
- request context終了後もworkerが続き、終了後はProvider lease、HTTP body、file、timer、goroutineを残さないこと。
- `go test ./...`、shuffle、race、vet、`go mod verify`、`govulncheck ./...`、CGO無効の
  Linux／Darwin amd64／arm64 build、Hosted Ubuntu CI。

## 実Android確認

固定したKomorebiを再現可能に導入し、接続先をEDCB、直接ライブを無効、画質をoption 10へ固定する。
公開候補のLinux amd64配布物と隔離data rootを使い、次を確認する。

- 主画面の開始、映像・音声、10分以上の継続、停止。
- 二画面目の開始、主画面と異なる放送、片方の停止・切替が他方を止めないこと。
- TvCastから4秒後にplaylistと3件以上の完成segmentを取得できること。
- segment取得を途中で切断し、Media3が単一Rangeで残りを再取得して再生を続けられること。
- GR、BS、CS 2Kのうち利用可能な代表serviceで、MPEG-2 VideoとH.264を分けて確認すること。
- provider切断後に古いsessionが終了し、固定クライアントの再試行で新しいsessionを開始できること。
- client停止後30秒でupstreamが閉じ、別の視聴を開始できること。
- 端末decoder、字幕、音声切替の結果をSazanamiのHTTP／HLS成功と分けること。

固定版を導入できなければ`NOT RUN: fixed Android client unavailable`とする。利用できる放送種別またはcodecが
なければ、項目ごとに`NOT RUN`理由を記録する。APIと合成TSだけでAndroid対応済みと表示しない。

報告にはsession件数、segment件数、転送byte数、playlist duration、所要時間、固定終了理由、成否だけを
残す。接続先、端末名、放送ID、局、番組、HLS key、file名、path、hash、TS内容、生の応答を残さない。

## 長時間確認

短い実Android確認を完了した同じ製品内容で、主画面または主画面＋二画面を含む連続運転をv0.9.0候補の
72時間試験へ組み込む。途中で実行時挙動を変えた場合は、新しい候補で最初からやり直す。72時間試験前は
v1.0対応済みと宣言しない。
