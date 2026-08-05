# 外部依存関係

Sazanami DVRはGo標準ライブラリに加え、CGOを使わずにSQLiteを扱うため、次のモジュールを使用します。正確なバージョンとチェックサムは`go.mod`と`go.sum`で固定しています。

| モジュール | バージョン | ライセンス |
|---|---:|---|
| `github.com/ncruces/go-sqlite3` | `v0.35.2` | MIT |
| `github.com/ncruces/go-sqlite3-wasm/v3` | `v3.2.35303` | MIT No Attribution |
| `github.com/ncruces/julianday` | `v1.0.0` | MIT |
| `golang.org/x/sys` | `v0.46.0` | BSD-3-Clause |

著作権表示とライセンス本文は[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)にまとめています。

## 更新時の確認

依存関係を更新するPull Requestでは、次を確認します。

1. 更新理由と対象バージョン
2. `go.sum`のチェックサム
3. ビルドに含まれるモジュールとライセンス
4. `govulncheck ./...`
5. 通常テスト、raceテスト、`go vet`、CGO無効のLinuxビルド
6. SQLiteの移行、バックアップ、復元

自動的な最新版追従は行いません。
