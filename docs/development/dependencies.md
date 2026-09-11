[README](../../README.md) / [開発者向け](README.md) / 依存関係

# 依存関係

Go標準ライブラリを基本にし、CGOなしでSQLiteを扱うための依存だけを使用します。

## Go

- Go: `1.26.0`
- toolchain: `go1.26.6`
- module定義: [go.mod](../../go.mod)

## Goモジュール

| モジュール | バージョン | 用途 | ライセンス |
|---|---:|---|---|
| `github.com/ncruces/go-sqlite3` | `v0.35.2` | CGOなしSQLite | MIT |
| `github.com/ncruces/go-sqlite3-wasm/v3` | `v3.2.35303` | SQLite補助 | MIT No Attribution |
| `github.com/ncruces/julianday` | `v1.0.0` | 日付計算 | MIT |
| `golang.org/x/sys` | `v0.46.0` | OS依存処理 | BSD-3-Clause |

著作権表示とライセンス本文は[THIRD_PARTY_NOTICES.md](../../THIRD_PARTY_NOTICES.md)を参照してください。

## コンテナ

[Dockerfile](../../Dockerfile)では、ビルド用と実行用のAlpineイメージを分けています。

## 依存を変更するとき

1. 標準ライブラリで代替できないか確認する
2. 追加理由と対象バージョンをPull Requestへ記載する
3. ライセンス表示を更新する
4. 通常テスト、raceテスト、`go vet`、CGO無効ビルドを実行する
5. SQLiteのmigration、backup、restoreへの影響を確認する

依存は自動的に最新版へ追従しません。
