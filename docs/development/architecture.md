[README](../../README.md) / [開発者向け](README.md) / アーキテクチャ

# アーキテクチャ

Sazanami DVRは、ドメイン、アプリケーション、外部接続を分けた単一プロセスのバックエンドです。

## 主なパッケージ

| 層 | 場所 | 役割 |
|---|---|---|
| CLI | `cmd/sazanami-dvr` | コマンドと起動設定 |
| ドメイン | `internal/core` | 番組、予約、録画、providerの型 |
| アプリケーション | `internal/app` | 同期、予約、録画、復旧 |
| CtrlCmd | `internal/adapters/ctrlcmd` | クライアント互換 |
| Mirakurun | `internal/adapters/provider/mirakurun` | 番組表とストリームの取得 |
| 録画HTTP | `internal/adapters/recordinghttp` | 録画、ライブ、ロゴのHTTP |
| WebUI | `internal/adapters/webui` | loopback限定の運用画面 |
| SQLite | `internal/adapters/sqlite` | DB、移行、バックアップ、復元 |
| ファイル | `internal/adapters/recordingfs` | 録画ファイルの検証と公開 |
| 起動設定 | `packaging` | systemd、Compose、インストーラ |

## 境界

- `internal/core`はCtrlCmd、HTTP、SQL、ファイル形式を扱わない
- CtrlCmdとMirakurunの形式は、それぞれのadapter内に閉じ込める
- 番組表の取得とTSの取得を分ける
- DBを予約と録画状態の正本にする
- 内部時刻はUTCで扱う
- 入力、応答、バッファ、キュー、同時接続に上限を設ける
- 外部入力は、状態変更や応答生成の前に検証する

## 接続の分担

```text
KonomiTV / Komorebi
        |
        +-- CtrlCmd / 録画HTTP
        |
        +-- application
                |
                +-- core
                +-- Mirakurun adapter
                +-- SQLite adapter

Webブラウザー -- WebUI（loopback限定）
```

録画サービスのCtrlCmdと録画HTTP、手動起動するWebUIは、それぞれ別の接続口です。

## 実装の確認先

- `internal/architecture/boundaries_test.go`
- `internal/architecture/doc_comments_test.go`
- `internal/adapters/ctrlcmd/runtime/runtime_test.go`
- `internal/adapters/recordinghttp/*_test.go`
- `internal/adapters/sqlite/*_test.go`
