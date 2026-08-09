# 録画ストリーム再接続仕様 第1版

- Status: Accepted
- Accepted date: 2026-08-09
- Decision owner: Project owner
- Delegated reviewer: Codex
- Related decision: [`../../docs/adr/0028-bounded-recording-stream-reconnect.md`](../../docs/adr/0028-bounded-recording-stream-reconnect.md)

## 目的

録画中の一時的な通信切断から、同じ録画処理の範囲内で安全に回復する。回復できない場合は部分録画を維持し、無制限の接続や再起動後の追記を行わない。

## 要件

| ID | 要件 |
|---|---|
| `BSR-001` | 一件の録画で同時に開くstreamは一つとし、追加接続は最大3回とする。 |
| `BSR-002` | 追加接続前に以前のleaseを閉じ、1秒、2秒、4秒の順に待つ。 |
| `BSR-003` | 親contextが有効で、予定終了まで60秒以上あり、失敗が再接続可能な場合だけ次を開く。 |
| `BSR-004` | 再接続後も同じattempt、部分ファイル、byte countへ順方向に書く。 |
| `BSR-005` | プロセス再起動後は部分ファイルへ追記せず、既存の復旧規則で終端へ進める。 |
| `BSR-006` | 再接続後の完了と上限到達を固定した終了理由で区別する。 |
| `BSR-007` | 接続先、番組情報、path、生の応答を通常出力へ含めない。 |

## 固定値

| 項目 | 値 |
|---|---:|
| 最初の接続 | 1回 |
| 追加接続 | 3回以下 |
| 待機 | 1秒、2秒、4秒 |
| 再接続を始める最小残り時間 | 60秒 |
| 同時stream | 1 |
| 追加goroutine | 0 |
| 追加queue | 0 |

既存のstream buffer 192,512 bytes、接続・header・read無進行10秒、予定終了時刻、shutdown 30秒を維持する。

## 再接続できる失敗

- 一時的な接続失敗。
- stream readのtimeout。
- 予定終了前のEOF。
- peer切断または0 byte進行。

次は再接続しない。

- 対象サービスなし。
- 要求拒否、3xx、認証相当の拒否、応答形式不正。
- 親contextの取消し、明示停止。
- DB更新、部分ファイル作成、write、sync、closeの失敗。
- 残り60秒未満、または追加3回を使用済み。

HTTP 5xxは一時的な接続失敗として扱う。404と要求不正は再接続しない。生のstatus bodyは保存も出力もしない。

## 処理順

1. 予約と録画処理をDBで一度だけ確保し、部分ファイルを一度だけ作る。
2. 同じservice、priority、descramble条件でstreamを開く。
3. 開けた最初のstreamで録画開始をDBへ記録する。
4. byteを既存bufferで部分ファイルへ書き、5秒以下の頻度で進捗を保存する。
5. 予定終了前に再接続可能な理由でstreamが終わった場合は、leaseを閉じる。
6. 残り時間と回数を確認し、対応する待機後に同じserviceへ接続する。
7. 新しいleaseから同じ部分ファイルへ書き続ける。
8. 予定終了へ達した場合だけ既存の完成処理を行う。

初回接続に失敗した場合も、再接続可能な理由なら同じ3回の追加接続を使える。`RecordingStarted`は最初にstreamを開けた時だけ記録する。

## 終了状態

| 状態／理由 | 条件 |
|---|---|
| `SUCCEEDED/COMPLETED` | 最初のstreamだけで予定終了へ到達 |
| `SUCCEEDED/COMPLETED_AFTER_RECONNECT` | 一回以上の追加接続後に予定終了へ到達 |
| `PARTIAL/STREAM_RECONNECT_EXHAUSTED` | 188 bytes以上を保存したが回復できない |
| `FAILED/STREAM_RECONNECT_EXHAUSTED` | 有用なbyteを保存する前に回復できない |
| 既存の取消し・file・DB理由 | 再接続対象外の失敗 |

再接続した完成録画は、予定時間内の欠損があり得る。TSの完全性や放送内容の連続性は保証しない。

## 保存と再起動

DB schemaは変更しない。ordinal 0のsegmentは部分ファイルと完成ファイルのライフサイクルを表し、HTTP接続回数は表さない。

プロセス停止では現在のleaseまたは待機を取り消し、部分ファイルをsyncして閉じる。次回起動では旧ownerの録画処理を既存規則で`CANCELLED`、`PARTIAL`、`FAILED`のいずれかへ進める。新しいstreamを開かず、旧部分ファイルへ追記しない。

## 必須テスト

### Unit

- 初回成功、1回・2回・3回目の成功、追加3回失敗。
- 待機1秒・2秒・4秒、4回目なし、同時接続1。
- 残り60秒ちょうどと1ns未満、予定終了、親取消し、待機中取消し。
- timeout、EOF、peer切断、0 byte、5xxを再接続する。
- 404、拒否、形式不正、file、DB、取消しを再接続しない。
- 同じ部分ファイル、累積byte count、進捗、`RecordingStarted`一回。
- 完了後のreasonと上限到達後のstate／reason。

### Integration and process

- `httptest`で接続ごとに成功、切断、stall、5xxを切り替える。
- 100回以上の切断混在でpanic 0、重複stream 0、goroutine／FD差分を確認する。
- shutdown、restart、既存部分ファイル、完成処理の全回帰試験。
- full、shuffle、race、vet、govulncheck、CGO無効4環境build、Hosted Ubuntu CI。

### 実験環境

- 録画中に一時的な切断を一回だけ発生させる。
- KonomiTVの予約一覧と録画中表示を維持する。
- 追加接続が上限内で成功し、録画が完成する。
- 完成録画をKonomiTVから再生開始できる。
- 件数、byte数、所要時間、終了理由、資源差分だけを記録する。

## 対象外

- 欠落パケットの補完、streamの巻き戻し、TS品質解析。
- 再起動後の追記、複数fileの結合。
- 複数同時録画、番組時刻追従、自動予約。
