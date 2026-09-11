[README](../../README.md) / [開発者向け](README.md) / テスト

# テスト

## Pull Request前の確認

Go 1.26.6で、変更範囲に応じて次を実行します。

```sh
gofmt -w cmd internal
go mod verify
go test -count=1 ./...
go test -count=1 -shuffle=on ./...
go test -count=1 -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
CGO_ENABLED=0 go build -trimpath ./cmd/sazanami-dvr
```

## 主なテスト範囲

| 場所 | 確認すること |
|---|---|
| `internal/architecture` | package境界、コメント、toolchainの不変条件 |
| `internal/adapters/ctrlcmd/runtime` | 固定profile、未対応番号、接続の継続 |
| `internal/adapters/ctrlcmd/*` | 各コマンドの形式、上限、不正入力 |
| `internal/adapters/recordinghttp` | 録画、Range、HLS、ロゴ、URL、接続数、権限 |
| `internal/adapters/provider/mirakurun` | URL、API、本文上限、timeout、ストリーム |
| `internal/adapters/sqlite` | migration、backup、restore、recover、ファイル保護 |
| `internal/app/recording` | 予約、録画開始、途中停止、再接続、終了 |
| `internal/app/catalogsync` | 番組表更新、失敗時の前回データ保持 |

## 結果の読み方

自動テストは合成データやfake providerを使った確認です。実Mirakurun、KonomiTVの画面、Komorebiの全画面、Android TV、実チューナーの動作は、それぞれの環境で別に確認します。
