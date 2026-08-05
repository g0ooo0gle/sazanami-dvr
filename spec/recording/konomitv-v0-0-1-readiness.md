# KonomiTV向けv0.0.1公開前仕様

- Status: Accepted
- Accepted by: Project owner
- Accepted date: 2026-08-06
- Applies to: Sazanami DVR v0.0.1 beta
- KonomiTV baseline: v0.14.1 / `0a32188274b81c1e7bed642474b208bd2a543a6b`

## 1. 目的

最初のベータ版では、KonomiTVから番組予約を追加し、録画中の状態を確認し、録画開始前の予約設定を
変更または取り消せるようにする。録画は番組開始前に短い準備時間を取り、開始遅れを減らす。

## 2. 対応するCtrlCmd

| Command | 番号 | 初版の動作 |
|---|---:|---|
| `CHG_RESERVE2` | 2015 | 一件の完全な予約を受け、対応済み録画設定だけを更新する |
| `DEL_RESERVE` | 1014 | 録画開始前の一件を取り消す |
| `NWPLAY_TF_OPEN` | 1087 | 対象予約が録画中なら安全な識別文字列を返す |
| `NWPLAY_CLOSE` | 1081 | 正のcontrol IDを受け、副作用なく成功する |

既存の2200、1060、1021、1029、2011、2013はそのまま維持する。2060、NetworkTV、録画済み情報command、
EDCB全体の互換性は対象外とする。

## 3. 予約変更

- Cmd2 versionは5だけを受ける。
- Vectorは一件だけを受ける。
- 予約番号は1以上のsigned 32-bit整数とする。
- KonomiTVが2011で取得した完全な予約情報と同じ形式を受ける。
- 番組と放送サービスの識別子、開始時刻、継続時間は、保存済み予約と完全に一致しなければならない。
- 初版で変更できる値は優先度1〜5と要求された番組追従flagだけとする。
- 実効の番組追従はoffのままとし、番組時刻を自動変更しない。
- 録画処理が一件でも作られた予約は変更しない。
- SQLiteへ確定した後だけ、result 1とversion 5を返す。
- 入力不正、不明な予約、競合、DB失敗はresult 0と空bodyを返す。

## 4. 予約取消し

- RequestはCtrlCmd vector形式のsigned 32-bit予約番号一件とし、bodyは正確に12 bytesとする。
- `ACTIVE`で録画処理がない予約だけを取り消す。
- 予約行とCtrlCmd番号の対応は削除しない。
- 予約を終了状態へ進め、取消理由`CANCELLED_BY_USER`と終了時刻を保存する。
- 録画処理、部分ファイル、完成ファイルは削除しない。
- SQLiteへ確定した後だけresult 1と空bodyを返す。
- 不明な予約、複数ID、録画開始後、再送、DB失敗はresult 0と空bodyを返す。

## 5. 録画中確認

- 1087のrequest bodyはsigned 32-bit予約番号一件、正確に4 bytesとする。
- 対象予約に`STARTING`、`RECORDING`、`FINALIZING`の録画処理がある場合だけ成功する。
- 成功bodyは一つの構造体とし、正のcontrol IDと固定文字列`sazanami-recording.ts`を返す。
- 実在するpath、録画root、番組名、予約内容を返さない。
- 1087はストリーム、ファイル、DB状態を変更しない。
- 1081のrequest bodyは正のsigned 32-bit control ID一件、正確に4 bytesとする。
- 1081は資源を解放する必要がないため、副作用なく成功する。
- 不正な長さ、0以下の値、不明または録画中でない予約はresult 0と空bodyを返す。

## 6. 録画開始の準備時間

- スケジューラは番組開始の5秒前を録画処理の起動時刻とする。
- 保存済みの番組開始時刻と予定終了時刻は変更しない。
- 番組開始5秒前を過ぎて追加された予約は、既存の遅刻条件を満たす限り直ちに開始する。
- 録画終了は番組の予定終了時刻とし、5秒の終了延長は行わない。
- 5秒は初版の固定値とし、利用者設定や新しいDB列を追加しない。

## 7. SQLite schema 4

`reservations`へnullableな`terminal_reason`列を追加する。値は1〜64 bytesの安定した理由文字列とし、
新しい理由を追加するたびにtableを作り直さなくてよい形にする。

- 既存の`FINISHED`予約は`ATTEMPT_FINISHED`へ更新する。
- 録画処理の終了で予約を閉じる場合は`ATTEMPT_FINISHED`を保存する。
- 利用者が1014で取り消す場合は`CANCELLED_BY_USER`を保存する。
- `ACTIVE`予約の理由はnullを維持する。
- 既存DBの更新は、明示的な`db migrate`と事前backupを必須とする。

## 8. 上限と失敗時の動作

- 既存のframe、string、vector、response、接続数、deadline上限を狭めない。
- 2015はrequest 1 MiB以内、成功bodyは正確に2 bytesとする。
- 1014、1087、1081は上記の正確なbody長を要求する。
- 失敗理由、DB内容、path、外部endpointをCtrlCmd応答や通常logへ含めない。
- 変更または取消しに失敗した場合は、直前に確定済みの予約を維持する。

## 9. 必須テスト

- 2015: 正常更新、version違い、0件／2件、予約番号0、不明番号、変更不可項目差分、録画開始後、
  unsupported設定、DB失敗、応答への秘密情報混入なし、再起動後の読戻し。
- 1014: 正常取消し、不明番号、0件／2件、負数、録画開始後、再送、DB失敗、録画file非削除。
- 1087／1081: 各録画状態、録画前後、不明番号、不正長、0以下のID、固定応答、追加stream接続なし。
- scheduler: 5秒前timer、準備時間内の追加、追加／変更／取消通知、遅刻境界、cancel、再起動。
- SQLite: schema 3から4へのbackup付きmigration、空DB、既存のcatalog・予約・録画結果の保持。
- 全体: full、shuffle、race、vet、govulncheck、Linux／macOSのamd64／arm64 CGO-off build、Hosted Ubuntu CI。

## 10. ベータ公開条件

次の全項目が同じ最終製品commitで成功した場合だけ、`v0.0.1`タグとGitHub Releaseを作る。

1. 必須自動テストとHosted CIが成功している。
2. 実験用Ubuntu環境のbackup付きmigrationが成功している。
3. KonomiTV v0.14.1で予約追加、録画中表示、予約変更、予約取消しを確認している。
4. 短い録画が完成し、再起動後もKonomiTVの録画一覧から再生を開始できる。
5. 宅内情報、番組情報、raw応答、録画TSがGitと公開文書に含まれていない。
6. READMEに初版の対応範囲と未対応範囲が自然な日本語で書かれている。

一項目でも確認できない場合は、sourceとcommitを維持したまま公開を延期する。
