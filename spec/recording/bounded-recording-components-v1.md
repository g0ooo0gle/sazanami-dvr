# 録画字幕・データ放送選別仕様 v1

- Status: Accepted
- Date: 2026-08-10
- Applies to: Sazanami DVR v0.0.21
- Decision: `docs/adr/0042-bounded-recording-component-filter.md`

## 目的

KonomiTV v0.14.1の字幕・データ放送設定を、CtrlCmd、SQLite、自動予約、録画TSで同じ意味にする。設定と実ファイルが異なる場合は成功にしない。

## 予約の値

予約は次のどちらかを持つ。

- `DEFAULT`: 全体既定を使う。実効値は字幕あり、データ放送なし。
- `EXPLICIT`: 字幕とデータ放送の二つをbooleanで持つ。

`DEFAULT`と、`EXPLICIT`の字幕あり・データ放送なしは実効値が同じでも区別する。2011で利用者の選択をそのまま読み戻すためである。

予約追加、変更、DB読取り、自動予約からの作成では、ほかの録画設定と同じtransactionで値を扱う。録画処理を確保した後の変更は、現在と同じく要求全体を失敗させる。

## CtrlCmdの正規化

2013と2015は`service_mode`について、`0x31`以外のbitが一つでも立っていれば`recording-setting-out-of-profile`として要求全体を失敗させる。

bit 0が立っていない`0x00`、`0x10`、`0x20`、`0x30`はすべて`DEFAULT`へ正規化する。bit 0がある値は次のように保存する。

| wire値 | 字幕 | データ放送 |
|---:|---|---|
| `0x01` | 含めない | 含めない |
| `0x11` | 含める | 含めない |
| `0x21` | 含めない | 含める |
| `0x31` | 含める | 含める |

2011は`DEFAULT`を`0x00`で返す。`EXPLICIT`はbit 0と二つの選択bitを組み立てて返す。wireの不要bitをDBへ保存しない。

## SQLite schema 10

`reservations`へ`component_mode`を追加する。値は`0`から`4`の整数に固定し、`0`は`DEFAULT`、`1`から`4`は明示4組を表す。旧予約は`0`へ移行する。

型、範囲、既定値をCHECK制約で確認する。schema 9から10への更新前backupを必須とし、backupをschema 9として復元できなければ更新完了にしない。通常起動で自動migrationしない。

## TS選別

実効値が字幕あり・データ放送ありなら、現在のstream copyをそのまま使う。それ以外は次の順で処理する。

1. read境界に依存せず188バイトのTS packetへ組み立てる。
2. 最初の有効なPATから、program番号0を除く一つのPMT PIDを特定する。
3. 対応PMT sectionを最後まで読み、section長とMPEG-2 CRC-32を確認する。
4. stream type `0x06`を字幕、`0x0D`をデータカルーセルとして選択を適用する。
5. 除外したentryをPMTから取り除き、section長、version、CRCを再生成する。
6. 除外したelementary PIDのpacketを保存しない。PCR PIDと、それ以外のstream typeは維持する。
7. PMTが更新された場合は新しいsectionを検証してから選択を更新する。

PATとPMTを確認するまではfileへ書かず、入力を最大1 MiBまで保持する。PSI sectionは1,024バイト、PMTのelementary streamは64件までとする。状態は録画streamの接続ごとに作り直し、再接続前の不完全sectionを持ち越さない。

出力は常に188バイトの倍数とする。出力byte数を録画履歴のbyte数に使う。最後に188バイト未満が残る、同期byteが違う、pointerやsection長が範囲外、PATの対象programが0件または2件以上、PMT PIDが一致しない、CRCが違う、上限を超える場合は`STREAM_FORMAT_INVALID`で終了する。この理由は再接続対象にしない。

PMT再生成時は元のprogram番号、PCR PID、program descriptor、維持するelementary stream descriptorを保つ。versionは元の値に1を足して5 bitへ収め、`current_next_indicator`を保つ。生成packetのcontinuity counterは最初の元PMT packetに合わせ、以後16で循環させる。

## 不変条件

- TS全体をメモリへ保持しない。
- 未確認のPIDを字幕・データ放送と推測して除外しない。
- 対応するPMTを確認する前に部分ファイルへ書かない。
- 設定、予約、録画処理、完成ファイルのどこか一つだけを成功させない。
- 完成済みファイルを上書きしない。保存ルート外へ書かない。
- ライブ視聴、カタログ同期、録画HTTP再生を変更しない。
- TS内容、番組情報、接続先、実pathを通常ログへ出さない。

## 必須テスト

- `DEFAULT`と明示4組のdomain検証、実効値、2013、2015、2011の往復。
- `0x00`、`0x10`、`0x20`、`0x30`の既定値への正規化。
- 未知bit、truncated、末尾byte、0件、2件で部分保存しない。
- 自動予約の対応設定を作成予約へ引き継ぎ、未対応設定では予約を作らない。
- schema 9→10、既定値、roundtrip、再起動、backup、restore、再migration、future、drift。
- 単一・複数packetのPAT／PMT、任意read分割、PMT更新、CRC、continuity counter。
- 字幕のみ、データのみ、両方、どちらもない、PCRと未知stream typeの維持。
- 同期byte、pointer、section長、CRC、program数、PMT PID、section 1,024バイト、stream 64件、保持1 MiBの境界と一件超過。
- EOF、切断、stall、cancel、再接続、file失敗、利用者停止でfile、lease、goroutineを残さない。
- 出力が188バイトの倍数で、履歴byte数と実fileが一致する。
- full、shuffle、race、vet、`go mod verify`、`govulncheck`、CGO無効のLinux／Darwin amd64／arm64 build、Hosted Ubuntu CI。
- 公開Linux amd64配布物で設定往復と短い実録画を行い、PMTに除外PIDがなく、残るPIDとpacketが整合する。

