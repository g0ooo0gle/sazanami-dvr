# CtrlCmdライブ中継仕様 v1

- Status: Accepted
- Date: 2026-08-09
- Requirements: `CLR-001`～`CLR-012`
- Related ADR: ADR-0034

## 目的

KonomiTV v0.14.1とKomorebi 1.1.0-beta6が使うCtrlCmd 1073、301、1074を、Mirakurun／mirakcの復号済みMPEG-TSへ安全に接続する。

## 要件

### CLR-001: 放送局の正本

1073で選べるのは、要求開始時に公開されている一つの完成済みチャンネルスナップショットに、ONID、TSID、SIDの組が一意に存在する放送局だけとする。照合結果からprovider locatorを取得し、要求値をMirakurunのservice IDとして直接使わない。

### CLR-002: 1073の要求

外側本文は26 byteの`SetChInfo`構造一つとする。構造extent 26、`use_sid=1`、正のONID、TSID、SID、`use_bon_ch=1`、正のNetworkTV ID、`ch_or_mode=2`だけを受け付ける。truncated、trailing、別mode、未知放送局、重複放送局を拒否する。

同じNetworkTV IDの古い利用がある場合は先に取り消して置き換える。新しい利用を含め同時4件までとし、空きがなければ応答を書き始める前に失敗する。

### CLR-003: 1073の応答

選択に成功した場合は`result=1`、本文4 byteの正のI32 process IDを返す。process IDはprocess内で一意とし、利用中または30秒の開始待ち中に再利用しない。NetworkTV IDをprocess IDとしてそのまま返す必要はない。

1073ではprovider streamを開かない。1073の応答完了から30秒以内に対応する301が始まらなければ、利用を自動的に破棄する。

### CLR-004: 301の要求と成功応答

301の要求本文は1073が返した正のI32 process ID一つだけとする。未知、終了済み、失効済み、すでに301で使用したIDを拒否する。

対応するprovider streamを一度だけ開く。開けた場合に限り、`result=1`、`body size=0`の8 byte headerを返す。以後は同じTCP接続へMPEG-TSを連続して書く。CtrlCmd frameを追加しない。

### CLR-005: 中継

providerへは照合済みlocator、利用目的`LIVE`、priority 0、復号必須、外へ公開しないbounded correlation IDを渡す。自動retryは行わない。

転送bufferは188 byteの整数倍で最大192,512 byteとする。読み取ったbyteだけを順に書き、TS全体または番組単位をメモリへ保持しない。providerがchunk境界を188 byteへ揃えることは前提にしない。

### CLR-006: 終了

301はクライアント書込失敗、provider終了・失敗・idle timeout、1074、親context取消し、process停止、開始から12時間経過のいずれかで終了する。終了時はprovider leaseをcancelしてcloseし、利用表から一度だけ削除する。

301接続の終了後にCtrlCmd失敗frameを追加してはならない。すでにTSを書いた後の失敗は接続終了で表す。

### CLR-007: 1074

要求本文は正のI32 NetworkTV ID一つだけとする。対応する利用があれば、開始待ちと配信中のどちらでも取り消す。未知または終了済みIDも冪等な終了として`result=1、body size=0`を返す。不正本文は拒否する。

### CLR-008: 同時実行と状態

状態は`SELECTED`と`STREAMING`の二つだけとする。同時利用は4件までとし、待ちqueueを作らない。NetworkTV IDの置換、301開始、1074、30秒失効が競合しても、一つの利用を二重開始または二重解放しない。

録画streamとライブstreamは別の同時数を持つ。ライブ4件が録画用adapterの枠を減らしてはならない。

### CLR-009: CtrlCmd server

受付serverはrouterが301を長時間接続として判定できる小さい能力を持つ。301だけは通常の14秒接続期限を外し、親contextと12時間上限で管理する。header読取2秒、本文読取14秒、最大接続32、最大handler 16、要求本文1 MiBは維持する。

単独の`ctrlcmd serve`にはライブ操作を接続しない。録画常駐processだけがProvider streamを所有する。

### CLR-010: 待受とLAN

既定はloopbackとする。録画常駐processで利用者がnumeric private IP、`0.0.0.0`、または`::`を明示した場合は、認証なしでLANから1073、301、1074を利用できる。hostname、multicast、link-local、global IP、port 0は拒否する。

LAN公開は信頼できる宅内ネットワークを前提とする。KonomiTVとKomorebiが送れない独自認証やTLSをCtrlCmdへ追加しない。

### CLR-011: 失敗と秘匿

ライブ操作はDB、番組表、予約、録画状態、録画fileを変更しない。通常出力へNetworkTV ID、process ID、provider locator、接続先、接続元、放送局情報、TS内容、生の要求と応答を出さない。errorは固定reasonへ変換する。

provider開始に失敗した301は成功headerを送らない。部分的に開始したprovider resourceは必ず閉じる。失敗後も別の利用を開始できる。

### CLR-012: 依存と版

実装はGo標準libraryと既存依存だけを使う。DB migration、新しい依存、CGO、framework、ORM、code generation、Node／npm、外部processを追加しない。製品versionをv0.0.10へ進める。

## 必須テスト

- KonomiTV固定版とKomorebi固定版の1073、301、1074要求byte列を別々に再現する契約test。
- 1073の正常、構造extent、各flag、各IDの0・負数・上限、truncated、trailing、未知放送局、重複放送局、同一NetworkTV ID置換。
- 1073の1～4件、5件目拒否、拒否後の既存利用維持、30秒直前・到達・直後、process IDの一意性と非再利用。
- 301の正常、本文長、未知・失効・終了・再利用process ID、provider拒否、wrong content type、3xx／4xx／5xx、header stall、body stall、disconnect。
- 301成功headerがprovider成功後であること、先頭chunk、複数chunk、短いchunk、write途中、client切断、cancel、12時間上限。
- 1074の未使用、SELECTED、STREAMING、重複、1073置換との競合、301開始との競合。
- ライブ4本と録画streamが独立して開けること、5本目拒否後と全終了後に新規利用できること。
- process停止時に全lease、HTTP body、socket、timer、goroutineが残らないこと。race detectorで利用表の競合がないこと。
- 通常commandの14秒上限と単独`ctrlcmd serve`のloopback限定・ライブ未対応が変わらないこと。
- `go test ./...`、shuffle、race、vet、`go mod verify`、`govulncheck ./...`、CGO無効のLinux／Darwin amd64／arm64 build、Hosted Ubuntu CI。

## 実環境確認

公開Linux amd64配布物を隔離data rootへ導入し、既存録画と番組表を壊さずに次を確認する。

- CtrlCmd直接接続で1073、301、先頭の188 byte以上、1074を一往復する。
- KonomiTV v0.14.1のライブAPIを開始・終了し、バックエンド起因のHTTP 5xxがないことを確認する。
- 可能なら異なる二局または同じ局の二接続を同時に開始し、片方の終了が他方を止めないことを確認する。
- 固定したKomorebiをAndroid TVへ導入できる場合は主画面と二画面目を確認する。導入できない場合は`NOT RUN: client installation unavailable`とする。

報告には件数、転送byte数、所要時間、終了理由だけを残す。接続先、番組情報、NetworkTV ID、process ID、TS内容、生の応答は残さない。
