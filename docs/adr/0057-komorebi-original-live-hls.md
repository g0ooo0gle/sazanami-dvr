# ADR-0057: Komorebiの非直接ライブは原画質TSをHLSへ詰め直す

- Status: Accepted
- Date: 2026-08-11
- Deciders: Project owner
- Delegated reviewer: Codex
- Related: ADR-0023、ADR-0026、ADR-0034、ADR-0056、Plan 0040、Plan 0072、Handoff 0048
- Product copy path: `docs/adr/0057-komorebi-original-live-hls.md`
- Product sync state: NOT COPIED

## 背景

Komorebi `1.1.0-beta6`は、EDCBへ直接接続し、直接ライブを無効にした場合、EDCB WebUI互換のHTTP serverへ
`/api/TvCast`、`POST /api/view`、`GET /api/view`を順に送る。主画面は`n=0`、二画面目は`n=1`を使う。
POSTから4秒待った後、GET URLをHLS MediaSourceへ渡し、playlistに記載されたsegmentを取得する。

これらのHTTP接続口はKonomiTVのAPIではない。Komorebi 1.0.0をKonomiTV経由で使う場合はKonomiTVが
HTTP serverを提供する。Sazanamiが実装する理由は、固定した1.1.0-beta6をEDCB直接接続として使う場合に限る。

製品はすでに、完成済みチャンネルから放送を解決し、Mirakurun／mirakcの復号済みservice streamを
上限付きで開く`internal/app/liverelay`を持つ。ただし現在はCtrlCmd 1073／301／1074へprogressive TSを
中継するだけで、HLS playlistとsegmentを作らない。

HLSでは、188 byteの倍数という条件だけでTSを任意に切れない。[RFC 8216](https://www.rfc-editor.org/rfc/rfc8216.html)
は、各MPEG-TS segmentを単一programにし、PATとPMTを含め、映像decoderを初期化できる情報を持たせる。
segment間のPCR時系列、PAT／PMT、trackまたはPIDが変わる場合は不連続として示す必要がある。Android Media3は
HLS内のMPEG-TSを扱えるが、sample codecの対応は端末decoderに依存する。

Project ownerは、Goだけで王道機能を優先し、v1.0までの技術判断をCodexへ委任した。本判断はその委任に
基づき、外部transcoderを使わない原画質HLSをv1.0候補として固定する。

## 判断

固定Komorebiが使う`GET /api/TvCast`、`POST /api/view`、`GET /api/view`と、serverがplaylistへ記載する
segment URLを録画常駐processのHTTP adapterへ追加する。

TvCastは完成済みチャンネルからONID、TSID、SIDを一意に解決し、`n=0`または`n=1`のslotへ選択する。
この段階ではprovider streamを開かない。POST viewで同じslotと放送を確認した後にだけstreamを開き、
HLS workerを録画常駐processのservice contextで開始する。HTTP request contextをworkerの寿命にしない。

resolverはADR-0056のoption 10一件を共用する。POST viewも`option=10`だけを受ける。この番号は
「オリジナル、変換なし」を意味し、画質変換済みであるとは表示しない。

入力MPEG-TSはGoで解析し、packetの内容と順序を変えずにHLS MPEG-TS segmentへ分ける。最低限、次を
満たすまでsegmentを公開しない。

- 188 byte packet同期が取れている。
- PATとPMTがCRC、version、continuityを含めて妥当である。
- 一つのmedia program、PCR PID、一つの対応video PIDへ解決できる。
- segment内に同じPATとPMTがある。
- video PIDのPES開始に先行する`random_access_indicator`がある。
- PCRから求めたdurationが正で、固定上限内である。

PATとPMTのversionはtableごとに現在採用中の値を持つ。両者のversion番号が一致することは求めない。
v1ではPTSを解析せず、durationと時系列の連続性はPCRだけから判断する。

入力を任意の188 byte境界で切らず、PAT／PMTやrandom accessが得られない場合はsessionを失敗させる。
元packetのPID、continuity counter、timestamp、payloadを変更しないため、この処理は動画変換ではなく、
同じTSをHLSの単位へ詰め直す処理である。

segmentは約1秒、最大2秒、32 MiB以下とする。現在と次の有効なsegment境界で得たPCRの差を
`EXTINF`へ使い、次の境界がない末尾は公開しない。playlistの`EXT-X-TARGETDURATION`は2秒で固定する。
表示windowは最大60秒で、6秒を下回らない。連番を再利用せず、PAT／PMT、trackまたはPID、PCR列が
変わる境界へ`EXT-X-DISCONTINUITY`を付ける。

完成segmentのGETは、Rangeなしを200、満たせる単一Rangeを206で返す。既存の完成file配信と同じく
`http.ServeContent`を使い、複数Rangeだけはmultipartへ広げず416とする。これにより、Android Media3が
segment受信の中断後に残りをRangeで取り直せる。

sessionは主画面と二画面の最大二件、cacheは一件512 MiB、合計1 GiBまでとする。合計には稼働中、作成途中、
retention中の全世代を含める。playlistから外したsegmentもRFC 8216のretentionを守る。容量上限を越える
場合はsegmentを早く削除せず、sessionを終了する。

segmentはdata root内のowner-only cacheへ置く。録画保存先へ混ぜず、DBへ保存しない。cacheの掃除は
固定directory内のSazanami自身の過去sessionだけに限り、symlinkをたどらない。

同じslot、放送、HLS keyへのPOST再送は冪等とする。別の放送またはkeyは古いsessionを閉じて置き換える。
provider切断、stall、最終HTTP accessから30秒、12時間上限、置換、shutdownでupstreamを閉じる。
未完成segmentは捨て、最後の完成segmentまでを`EXT-X-ENDLIST`付きでretention期間だけ維持する。

終了後のkeyを再利用しないため、process内に最大4,096件のkeyを記録する。上限到達後に新しいkeyを受けた
場合は503 `session-key-limit`とする。既存sessionとretention中の読み出しは継続し、key自体は出力しない。

画質変換、deinterlace、frame rate変換、codec変換、ABR、字幕焼込みには対応しない。端末が元codecを
decodeできない場合も、対応済みへ丸めない。

## 所有境界

HTTP wire形式、query／form／Cookie、HLS session、playlist、segment配信は
`internal/adapters/recordinghttp`へ閉じ込める。TS packet、PSI、PAT、PMT、MPEG-2 CRC32の処理は、
`internal/app/recording/ts_filter.go`にある既存実装を重複させない。両方から使える小さい内部packageへ
必要なprimitiveだけを抽出し、録画filterとHLS parserで共有する。汎用media frameworkやplugin機構は
作らない。

放送の一意な解決とprovider streamの開始・終了には、HLS専用の`liverelay.Manager`を使う。
HTTP adapterへ渡す能力は`Select`、`Open`、`Close`に限る。Mirakurun DTO、HTTP client、provider locatorを
HLS parserへ渡さない。domain、SQLite、録画file、schedulerはTvCast、view、HLSを知らない。

KonomiTV経由のKomorebi 1.0.0、Mirakurun直接設定、CtrlCmd直接ライブは既存経路を維持し、本HTTP経路へ
回さない。利用者はKomorebi側で接続方法を明示的に選べる。

## 結果

- 固定版KomorebiのEDCB非直接ライブに必要なHTTP serverの責任がSazanami側で揃う。
- 主画面と二画面を原画質のままHLSとして再生できる候補になる。
- 外部transcoder、別daemon、DB migration、新しい依存を追加しない。
- HLS固有の境界判定、cache、session worker、時間と容量上限が新しく必要になる。
- TSのpacket／PSI primitiveは録画filterと共有し、同じ検証処理を二重実装しない。
- random accessを持たないstream、multi-program TS、対応外codecは失敗する。
- MPEG-TSを保持しても、ARIB字幕などを固定クライアントが表示できるとは限らない。
- Android実機で主画面、二画面、代表的なGR／BS／CS 2Kを確認するまで対応済みとは表示できない。

## 採用しなかった案

### GET viewからprogressive TSをそのまま返す

固定版はこの経路をHLS MediaSourceへ渡す。playlistではない応答は契約を満たさず、HTTP 2xxだけの
偽対応になるため採用しない。

### 188 byteごと、または一定byte数ごとにsegmentを切る

PAT／PMT、random access、timestampを保証できず、clientがbufferingのままになる可能性がある。
HLSのMPEG-TS要件を満たさないため採用しない。

### ffmpegなどの外部transcoderを起動する

画質変換はできるが、外部実行ファイルの導入、process監視、資源見積り、license、配布、障害点が増える。
Go onlyという製品方針に反するためv1.0範囲では採用しない。

### segmentをmemoryだけに置く

二画面、60秒window、retentionを合わせるとmemory上限が大きくなり、再送中のsegmentを安全に保持しにくい。
owner-onlyの有界cache fileを使う。

### 一つのsessionを主画面と二画面で共有する

固定版は別の`n`とHLS keyを使い、別放送を同時再生できる。一方の停止や切替が他方を止めないよう、
二slotを独立させる。

### 画質名だけ複数返し、すべて原画質へ丸める

利用者の選択と実際の映像が一致しない。option 10一件だけを表示する。

## 検証

- 固定KomorebiのTvCast、view、token、form、Cookie、4秒待機、HLS再生をsourceで照合する。
- 共通TS primitiveを、chunk境界、sync、PAT／PMT、CRC、continuity、PSI version 31から0への循環で確認し、
  既存の録画filterも同じtestで回帰しないことを確認する。
- 生成したsegmentを再解析し、一program、PAT、PMT、188 byte倍数、random access、次の境界PCRから求めた
  durationを確認する。次の有効境界がない末尾は破棄されることも確認する。
- segment GETの200、単一Rangeの206、複数Rangeの416と、Media3相当の中断後Range再試行を確認する。
- playlistのContent-Type、sequence、target duration、window、retention、discontinuity、ENDLISTを確認する。
- 二slot、冪等再送、置換、切断、idle、shutdown、上限超過でfile、timer、goroutine、leaseを残さない。
- 固定Androidクライアントで主画面、二画面、切替、停止、継続、segment受信中断後のRange再試行、
  provider切断後のsession再試行を確認する。
- GR／BS／CS 2Kごとにsource、合成test、実機結果を分け、端末codec依存を記録する。
- 72時間試験はv0.9.0候補と同じ製品内容で別に行う。

## 製品同期

- Handoff: Handoff 0048
- Planning source commit: Handoff 0048で固定する
- Design inspection base commit: `6443e5e6b9dba5f207a5f913ecc307f5819670c8`
- Target product base commit: `5deb65c9f0604d1a089a85d32123e27de7f72e28`
- Product destination: `docs/adr/0057-komorebi-original-live-hls.md`
- Last synchronized product commit: NOT COPIED
- Known divergence: 製品はCtrlCmdのprogressive live relayだけを持ち、TvCast、view、HLSを公開しない

## 見直す条件

- 対象放送で`random_access_indicator`が得られず、codec別access unit解析が必要と分かる。
- 2秒の固定上限内にrandom accessが現れない正常streamが確認される。
- 固定Androidクライアントが元codec、PAT／PMT、segment duration、Cookieの別条件を必要とする。
- 画質変換をv1.0必須へ加える新しい利用者判断がある。
- 対象KomorebiがTvCast、view、HLSの呼び出しを変更する。
