# ADR-0056: Komorebiのxcode option 10は完成録画を変換せず返す

- Status: Accepted
- Date: 2026-08-11
- Deciders: Project owner
- Delegated reviewer: Codex
- Related: ADR-0023、ADR-0026、ADR-0033、ADR-0053、Plan 0040、Plan 0071、Handoff 0047
- Product copy path: `docs/adr/0056-komorebi-original-xcode-passthrough.md`
- Product sync state: NOT COPIED

## 背景

Komorebi `1.1.0-beta6`は、EDCBへ直接接続しながら録画再生方法にHTTP APIを選ぶと、
`resolver.lua`の画質一覧から番号を選び、`/api/xcode`へ送る。固定ソースの
`EdcbRecordRepository.getRecordStreamUrl`は、resolverの`video_url`を`fname`として使う分岐と、
録画番号を`id`として使う分岐を持つ。再生位置がある場合は`ofssec`も送る。

同じ固定版の`VideoPlayerViewModel.resolveStreamUrl`は、URLが`/api/xcode`でも選択値が`10`なら
ライブ変換として扱わない。録画画面は通常のMPEG-TS再生とHTTP Rangeによるシークを使う。
したがって、option 10は完成録画を変換せず返す契約として実装できる。

製品にはすでに、`HistoryItem.Playable()`で正常終了と安全に確定した利用者停止を一つの判定にまとめ、
安全に開いた通常fileを`video/mp2t`でRange配信する`internal/adapters/recordinghttp`がある。
新しいtranscoderや別のfile公開機構は不要である。

Project ownerはv1.0までの技術判断をCodexへ委任し、王道機能を先に、Goだけでシンプルに実装する方針を
採用した。本判断はその委任に基づき、v0.9.0までに必要なKomorebi直接接続の録画再生範囲を固定する。

## 判断

引数なしの`GET /komorebi/resolver.lua`は、空の`ctok.xcode`、空の`ctok.view`と、次の選択肢一件だけを
返す。

| id | 表示名 | 意味 |
|---|---|---|
| `10` | オリジナル（変換なし） | 再生可能として安全に確定したMPEG-TSを保存内容のまま返す |

`GET`／`HEAD /api/xcode`を録画HTTP adapterへ追加する。要求は次のどちらか一方で完成録画を選ぶ。

- `fname=recordings/{id}.ts`
- `id={id}`

`fname`はSazanamiがresolverで公開する仮想pathだけを受ける。hostの絶対path、DB内の相対path、
EDCBの録画directory、任意のfile名は受けない。`fname`と`id`の重複、同時指定、未知queryも拒否する。

`option`は文字列`10`を一つだけ必須とする。他の値を10へ読み替えず、安定したunsupported応答にする。
`ctok`は固定版がresolverの値をそのまま送るため、省略または空文字一件だけを受ける。これは認証ではない。
非空token、重複token、別のtokenを受理しない。

`ofssec`は省略、または0以上の10進整数一件だけを受ける。option 10では内容開始位置へ反映しない。
固定版はoption 10を通常録画として扱い、再生再開とシークにRangeを使うためである。時刻を平均bitrateで
byteへ変換する処理を重ねない。

配信可否は既存の`HistoryItem.Playable()`を共有する。予定どおり正常終了した録画に加え、利用者が停止し、
`PARTIAL/USER_REQUESTED_STOP`として安全に完成名へ確定した録画も再生できる。どちらもsegmentの完成、
公開flag、最小byte数、通常file、所有者、file mode、symlink拒否、DB byte数との一致を確認する。
録画中、安全確定前、復旧途中、利用者停止以外の部分録画、失敗、欠損、不整合は配信しない。
`/recordings/{id}.ts`と`/api/xcode`は同じ同時8配信枠を使う。

成功応答は`Content-Type: video/mp2t`、`Content-Length`、`Accept-Ranges: bytes`を持ち、全体、HEAD、
単一Range、206、416を標準HTTPの意味で返す。複数Rangeは拒否する。redirectや内部pathを返さない。

この対応は「原画質、変換なし」とだけ表示する。解像度、bitrate、frame rate、codec、deinterlace、
字幕焼込み、複数画質、ABRには対応しない。未対応の画質をresolverへ表示しない。

## 所有境界

このHTTP serverを提供するのはSazanamiの録画常駐processである。KonomiTV経由のKomorebi 1.0.0では、
KonomiTVが自身のHTTP APIと再生streamを提供するため、本経路をSazanamiへ実装する理由にはしない。
対象は固定したKomorebi `1.1.0-beta6`で、接続先にEDCB、録画再生方法にHTTP APIを選んだ場合だけである。

wire形式とquery検証は`internal/adapters/recordinghttp`へ閉じ込める。domain、SQLite、録画保存先は
`/api/xcode`、Komorebi、EDCBの語を知らない。DB migration、別daemon、新しいproduct packageを
追加しない。

## 結果

- KomorebiのEDCB直接接続で、HTTP API設定から完成録画を原画質再生できる入口が揃う。
- 既存の`HistoryItem.Playable()`、file検証、Range、同時数を共有し、二つの配信実装がずれにくい。
- 動画変換、一時file、外部process、DB変更、新しい依存を追加しない。
- `ofssec`自体による時刻シークは提供しない。固定クライアントではRangeによる再開とシークを確認する。
- option 10で端末がdecodeできないcodecは、サーバー側で変換しないため再生できない可能性がある。

## 採用しなかった案

### `/api/xcode`から`/recordings/{id}.ts`へredirectする

固定版はredirectを許す設定を持つが、Range header、失敗応答、公開URLの意味が中継先に依存する。
同じhelperから直接配信すれば追加の通信もなく、契約を一か所で確認できるため採用しない。

### `ofssec`を録画時間とfile sizeの比率でbyte位置へ変換する

可変bitrateでは時刻とbyte位置が一致せず、TS packetやキーフレームの途中から返す可能性がある。
さらに固定版がRangeシークを重ねるため採用しない。

### 一時processでoption 10も変換する

保存内容をそのまま再生する契約に不要であり、Go only、新しい外部実行ファイルなしという方針にも反する。

### 一般的なEDCB WebUIのxcode互換を実装する

任意の`fname`、複数option、外部encoder、進捗、停止など、固定版が必要としない大きな面になる。
今回の対応はSazanamiの仮想pathとoption 10へ限定する。

## 検証

- 固定Komorebiのresolver、URL生成、非ライブ判定、MPEG-TS再生とRange処理をsourceで照合する。
- resolver、`fname`／`id`、query、method、token、`ofssec`、optionの契約testを行う。
- 正常終了と安全確定した利用者停止を配信し、それ以外の部分録画、失敗中、安全確定前、復旧途中を
  拒否することを確認する。
- file検証、全体、HEAD、Range、同時数、切断、cancel、resource解放を確認する。
- `/recordings`の既存testと同時実行し、二経路の条件が一致することを確認する。
- 固定Androidクライアントで、選択肢、再生、再開、前後移動、末尾までの再生を確認する。
- source確認、合成test、Android実機の証拠を互換実装表で分ける。

## 製品同期

- Handoff: Handoff 0047
- Planning source commit: Handoff 0047で固定する
- Target product base commit: `04fd71ff33ef7f0f1a41f96d151efe19dee7c52b`
- Product destination: `docs/adr/0056-komorebi-original-xcode-passthrough.md`
- Last synchronized product commit: NOT COPIED
- Known divergence: 製品の既存resolverは`option`が空で、`/api/xcode`を公開しない

## 見直す条件

- 固定版のAndroid検証で、option 10でも`ofssec`による開始位置変更が必須と分かる。
- Rangeだけでは実用的なシークができず、録画時刻indexの必要性が証明される。
- 対象Komorebiがresolverの選択肢、query、再生処理を変更する。
- Goだけで実用的な画質変換を行う別のAccepted判断が作られる。
