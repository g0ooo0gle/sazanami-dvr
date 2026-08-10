# KonomiTV予約変更の予定ファイル名折返し仕様 第1版

- Status: Accepted
- Accepted date: 2026-08-10
- Decision owner: Project owner
- Delegated reviewer: Codex
- Related decision: [`../../docs/adr/0041-safe-reservation-output-paths.md`](../../docs/adr/0041-safe-reservation-output-paths.md)
- Extends: [`safe-reservation-output-v1.md`](safe-reservation-output-v1.md)

## 目的

KonomiTV v0.14.1が2011で受け取った予定ファイル名を2015へ送り返しても、保存先付き予約を安全に変更できるようにする。

## 要件

| ID | 要件 |
|---|---|
| `ROUE-001` | 2015の`rec_file_name_list`は0件または1件を受け付ける。 |
| `ROUE-002` | 一件の値は通信上の検証と上限を満たすまで読み取るが、予約設定、保存先、ファイル計画として使用しない。 |
| `ROUE-003` | 保存先とファイル名テンプレートの正本は、検証済みの`rec_folder_list`と既存DB値だけとする。 |
| `ROUE-004` | 2013の`rec_file_name_list`は引き続き0件だけを受け付ける。 |
| `ROUE-005` | 件数超過、不正な長さ、途中で切れた値、本文上限超過は、予約を変更せず安定した理由で失敗させる。 |
| `ROUE-006` | 2015の他のサーバー管理項目に対する既存の空値制約を緩めない。 |

## 入力の扱い

2011は、ファイル名テンプレートがある予約へ`rec_file_name_list`を一件返す。KonomiTVは、その予約を2015へ送り返すことがある。2015はこの一件を互換用の折返し値として読み捨てる。

文字列が絶対pathに見える場合や、現在の予定名と異なる場合も、保存先の指定として扱わない。値をDB、通常ログ、エラー応答へ残さない。予約の出力設定は、同じ要求内の`rec_folder_list`を従来の録画先仕様で検証した結果だけから更新する。

2013には折返し元がないため、値が一件でもあれば`reservation-server-field`として失敗させる。2015の2件以上も同じ理由で失敗させる。vectorと文字列の既存上限を変更しない。

## 変更しない範囲

- DB schemaとmigration。
- 2011が返す予定ファイル名の形式。
- `rec_folder_list`、相対フォルダー、テンプレート、ファイル計画の検証。
- 録画開始後の予約変更禁止。
- 予約番号、番組、時刻など、2015で変更できない項目の照合。
- KonomiTV以外の新しい互換表明。

## 必須テスト

- 保存先とテンプレート付きの予約を2013で追加し、2011で予定名一件を読み、同じ値を含む2015で優先度を変更できる。
- 2015は予定名0件と1件を受け付け、予約の出力設定を`rec_folder_list`どおりに保存する。
- 2015へ別の相対名や絶対pathに見える値を送っても、その値を保存、使用、通常ログ出力しない。
- 2013は予定名一件を拒否する。
- 2015は予定名2件、不正な長さ、途中で切れた文字列、本文上限超過を拒否し、予約を部分変更しない。
- 既存の2013、2015、2011、保存先、録画、DB、診断の回帰testを行う。
- full、shuffle、race、vet、`go mod verify`、`govulncheck`、CGO無効のLinux／Darwin amd64／arm64 build、Hosted Ubuntu CIを行う。
- 実験環境の固定KonomiTVから、保存先付き予約の追加、読戻し、変更、削除を行う。件数とHTTP状態だけを記録し、生の要求・応答、path、番組情報を残さない。

