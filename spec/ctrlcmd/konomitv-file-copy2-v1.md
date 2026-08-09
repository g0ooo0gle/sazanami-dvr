# KonomiTV向けFILE_COPY2 2060の最小互換仕様

- Artifact status: Accepted
- Accepted: 2026-08-07
- Human authorization: Project ownerは2026-08-07、v1.0までのクライアント互換、DB、公開API、実験環境、安定版公開に関する技術判断をCodexへ委任し、個別承認を待たずに進めることを明示した
- Delegated decision: 上記委任を本仕様へ適用し、許可するファイル名2件、固定INI、通信形式、失敗応答、処理上限、完了条件を採用した
- Authorization record: `docs/plans/0040-v1-0-delivery.md`の「目標」
- Target client: KonomiTV `v0.14.1` / `0a32188274b81c1e7bed642474b208bd2a543a6b`
- Product sync: Handoffで指定した製品commitへ同じpathでcopyする
- Extension: [`bounded-service-logo-v1.md`](bounded-service-logo-v1.md)が、許可したロゴ要求だけを追加する。固定INI 2件の内容と不変条件は本仕様を維持する

## 目的

KonomiTVが録画設定画面と予約一覧で取得する`EpgTimerSrv.ini`と`Bitrate.ini`だけを、EDCB互換コマンド`FILE_COPY2` 2060で返す。任意ファイルの取得、ディレクトリ一覧、ロゴ配信、実ホストの設定ファイル公開には広げない。

これは2060の最初の実装単位である。KonomiTVが局ロゴの代替取得で使う`LogoData.ini`と`LogoData\\*.*`は、KonomiTV全操作を仕上げる後続仕様で扱う。

## 要件

| ID | 要件 |
|---|---|
| `KFC-001` | コマンド2060で、許可した2つのファイル名に限り固定データを返す |
| `KFC-002` | `Cmd2`バージョン5、配列、`FileData`の通信形式と失敗応答をこの仕様どおりに固定する |
| `KFC-003` | 2つの固定INIを返す処理ではDB、ネットワーク、実ファイルへ接続せず、要求512バイト、応答8 MiBの上限を守る。ロゴ要求は拡張仕様を正本とする |
| `KFC-004` | 対象版KonomiTVで警告を増やさず、既定プリセットから予約・録画できることを同じ製品コミットで確認する |

## 受け付ける要求

CtrlCmdの外側のコマンド番号は2060とする。本文には、`Cmd2`バージョン5を表す`U16LE`とUTF-16LEの文字列配列を、この順で格納する。配列の要素数は1件のみとし、次のファイル名との完全一致だけを受け付ける。

- `Bitrate.ini`
- `EpgTimerSrv.ini`

要求本文の形式は次のとおりとする。すべての整数はリトルエンディアンであり、extentは先頭の4バイトを含む。

| 順序 | 型 | 値と条件 |
|---:|---|---|
| 1 | `U16LE` | `Cmd2`バージョン。5だけを受け付ける |
| 2 | `I32LE` | 文字列配列のextent。8以上で、配列全体の末尾と一致する |
| 3 | `I32LE` | 要素数。1だけを受け付ける |
| 4 | `I32LE` | 文字列のextent。6以上の偶数で、文字列の末尾と一致する |
| 5 | UTF-16LE | 許可したファイル名。BOMと途中のNULを含めない |
| 6 | `U16LE` | 文字列末尾のNUL。1個だけ置く |

ファイル名の大文字・小文字が異なる場合や、絶対パス、パス区切り文字、`..`、NUL、ワイルドカードを含む場合は拒否する。配列が空、または2件以上の場合と、バージョン5以外の場合も拒否する。要求本文の上限は512バイトとし、各文字列には既存のCtrlCmd codecの長さ上限も適用する。

形式が正しくても許可名でない要求と、バージョン5以外の要求には、外側の`result`が203、本文0バイトの未対応応答を返す。壊れたフレーム、上限超過、途中切断、期限切れは応答を送らず、1要求1応答の接続を閉じる。応答やエラーへ要求内容を含めない。

## 応答形式

成功時は、外側の`result`を1とする。本文は次の順序で構成する。すべての整数はリトルエンディアンであり、各extentは先頭の4バイトを含む。

| 順序 | 型 | 値と条件 |
|---:|---|---|
| 1 | `U16LE` | 応答バージョン5 |
| 2 | `I32LE` | `FileData`配列のextent。8以上で、本文末尾と一致する |
| 3 | `I32LE` | 要素数1 |
| 4 | `I32LE` | `FileData`構造体のextent。4以上で、構造体末尾と一致する |
| 5 | `I32LE` | ファイル名文字列のextent。6以上の偶数 |
| 6 | UTF-16LE | 要求と同じファイル名。BOMを含めない |
| 7 | `U16LE` | ファイル名末尾のNUL。1個だけ置く |
| 8 | `I32LE` | データのバイト数。0以上で、実際のデータ長と一致する |
| 9 | `I32LE` | 未使用の`reserved`領域。0に固定する |
| 10 | バイト列 | 固定INIのデータ本体 |

本文と各extentに余剰バイトを付けない。`FileData`配列と構造体は、宣言した件数と末尾を正確に一致させる。

応答全体のバイト数を先に計算し、書き始めた後の上限超過を起こさない。本文は8 MiB以下とし、今回の固定データはそれより十分小さく保つ。処理の取り消しまたは書き込み失敗時は接続を閉じ、第2の応答を追加しない。

## 失敗の扱い

| 条件 | 外側の`result` | 本文 | 接続と安定した内部理由 |
|---|---:|---:|---|
| 許可名以外 | 203 | 0バイト | 応答後に閉じる。`file-name-out-of-profile` |
| バージョン5以外 | 203 | 0バイト | 応答後に閉じる。`version-out-of-profile` |
| コマンド2060以外 | 応答なし | なし | ルーターの誤配送として閉じる。`command-out-of-profile` |
| 本文の不足、壊れたextent、余剰バイト | 応答なし | なし | 閉じる。既存codecの`TRUNCATED`または`MALFORMED` |
| 本文、文字列、配列の上限超過 | 応答なし | なし | 閉じる。既存codecの`OVER_LIMIT` |
| 期限切れ、取り消し、相手側の切断 | 応答なし | なし | 閉じる。既存codecの`TIMEOUT`または`PEER_DISCONNECT` |
| 応答の書き込み失敗 | 送信済みの範囲だけ | 送信済みの範囲だけ | 直ちに閉じ、第2応答を送らない。`PEER_DISCONNECT`または`INTERNAL` |

codecの形状、上限、検査順序、エラー分類は、製品へ複製済みの`spec/ctrlcmd/framing-and-primitives.md`、`spec/ctrlcmd/codec-limits.md`、`spec/ctrlcmd/go-codec-binding.md`を正本とする。この仕様の512バイトと8 MiBは、それらより小さいコマンド固有上限として適用する。Draftの`spec/ctrlcmd/limits.md`は実装根拠にしない。

## 固定データ

データは製品内の定数から生成し、OS上のファイルを読まない。UTF-8 BOM付きのINIとして返す。

`Bitrate.ini`は`BITRATE`セクションだけを持つ。サービスごとの値は持たせず、KonomiTV v0.14.1の既定値19,456 kbpsを使わせる。これにより取得失敗の警告を消しつつ、測定していないサービス別のビットレートを作らない。内容は次のとおりとし、改行はCRLFに固定する。

```ini
[BITRATE]
```

`EpgTimerSrv.ini`には既定プリセットを1つだけ定義する。録画モードは指定サービス、優先度は3とし、番組追従、ぴったり録画、個別余白、連続ファイル化、ワンセグ分離、チューナー指定は無効にする。録画後の動作は全体設定に従う。全体設定は、開始余白5秒、終了余白2秒、字幕あり、データ放送なし、録画後は何もしない、とする。録画フォルダやホスト固有のパスは含めない。内容は次のとおりとし、改行はCRLFに固定する。

```ini
[SET]
StartMargin=5
EndMargin=2
Caption=1
Data=0
RecEndMode=0
Reboot=0
PresetID=

[REC_DEF]
SetName=デフォルト
RecMode=1
NoRecMode=1
Priority=3
TuijyuuFlag=0
ServiceMode=0
PittariFlag=0
BatFilePath=
SuspendMode=0
RebootFlag=0
UseMargineFlag=0
StartMargine=0
EndMargine=0
ContinueRec=0
PartialRec=0
TunerID=0
```

両ファイルは上の内容の先頭にUTF-8 BOMを付け、末尾にもCRLFを一つ置く。

KonomiTVが未対応項目を変更して予約を送った場合は、既存の予約入力検証が失敗応答を返す。設定ファイルを返したことだけで、Sazanami DVRがすべての録画設定を受け付けるとは表明しない。

## 根拠と採用判断

通信形式は、KonomiTVの固定コミットにある次の記号を読み取り専用で確認した。

- `server/app/utils/edcb/CtrlCmdUtil.py::sendFileCopy2`
- `server/app/utils/edcb/CtrlCmdUtil.py::__sendCmd2`
- `server/app/utils/edcb/CtrlCmdUtil.py::__writeVector`
- `server/app/utils/edcb/CtrlCmdUtil.py::__writeString`
- `server/app/utils/edcb/CtrlCmdUtil.py::__readVector`
- `server/app/utils/edcb/CtrlCmdUtil.py::__readStructIntro`
- `server/app/utils/edcb/CtrlCmdUtil.py::__readFileData`

利用方法と既定値は、`server/app/routers/ReservationsRouter.py::DecodeEDCBReserveData.GetBitrateFromEDCB`、`server/app/routers/RecordingPresetsRouter.py::RecordingPresetsAPI`、同ファイルの`ParseGlobalDefaults`と`ParsePreset`で確認した。KonomiTVが取得失敗時に使う19,456 kbpsはクライアント側の動作であり、Sazanamiが測定した値ではない。

固定INIの内容、許可名を2件に絞ること、ロゴを後続へ分けること、512バイトと8 MiBの上限はSazanami側の採用判断である。外部ソースの文面やバイト列は製品へコピーしない。

## 不変条件

- 2つの固定INIを返す処理はDB、ネットワーク、実ファイル、環境変数へ接続しない。ロゴ拡張は別仕様の固定名、完成済みスナップショット、上限付きMirakurun接続だけを使う。
- 許可名以外の内容やホスト情報を返さない。
- 既存の1060 `ChSet5.txt`処理と責任を混ぜない。
- ロゴ拡張を持たない製品版では、ロゴ取得ワイルドカードを成功にしない。
- 応答は毎回同じバイト列とし、同じ製品バージョン内で変化させない。

## 必須テスト

- 2つの許可名について、バージョン、配列、構造体、ファイル名、データのバイト数、`reserved`領域、INI内容を最後までデコードする。
- KonomiTV v0.14.1相当の読み取り処理で2つの応答を読めることを確認する。
- 未知の名前、大文字・小文字の違い、`..`による親ディレクトリ参照、スラッシュ、バックスラッシュ、ワイルドカード、NUL、空配列、要素数が2件以上の入力を拒否する。
- バージョン違い、途中切断、余剰バイト、壊れた長さ、負数、要素数が上限を1件超える入力、本文が上限を1バイト超える入力を拒否する。
- 取り消し、期限切れ、短い書き込み、切断、依存先がない状態でpanicや秘密情報漏えいを起こさない。
- 2060を呼んでもDB、番組情報の提供元、ファイルシステムへ接続しない。
- 既存の2200、1060、1021、1029、2011、2013、2015、1014、1087、1081の回帰テストを維持する。

## 実環境確認

対象版KonomiTV v0.14.1で録画プリセットAPIと予約一覧を開き、`EpgTimerSrv.ini`と`Bitrate.ini`の取得失敗警告が新規に出ないことを確認する。表示された既定プリセットから1件を予約し、既存の録画成功経路を維持する。実ファイル、生の応答、番組情報、宅内識別情報は保存しない。

## 完了条件

- 必須テストがすべて成功し、既存のCtrlCmd回帰テストにも失敗がない。
- KonomiTVの録画プリセットAPIがHTTP 200を返し、二つの設定ファイルの取得失敗警告が新規に出ない。
- 表示した既定プリセットから予約を追加でき、既存の録画完了経路を維持している。
- 実装、公開文書、実環境結果が同じ完全な製品コミットへ結び付いている。
