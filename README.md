# Sazanami DVR

Sazanami DVRは、Mirakurun／mirakcから番組情報と放送ストリームを受け取り、KonomiTVからの予約を録画する軽量なバックエンドです。Goで実装しており、一つの実行ファイルで動きます。

現在のバージョンは **v0.0.1（初期プレビュー）** です。基本的な流れは動きますが、対応範囲はまだ限定的です。

## 現在できること

- Mirakurun互換APIからサービスと番組を取得し、SQLiteへ保存する
- KonomiTV向けに、状態確認、チャンネル一覧、番組表、予約一覧、予約追加を返す
- 予約時刻にMirakurunから放送ストリームを受け取り、TSファイルへ保存する
- 再起動後も予約と録画結果を引き継ぐ
- 保存済みの番組表と運用状態を、同じPCのWebUIで確認する
- DBの移行、バックアップ、復元を明示的なコマンドで行う

MirakurunとKonomiTV v0.14.1を使い、番組表の表示、予約、5分間の録画、再起動後の状態確認、録画済み一覧からの再生を一件だけ確認しています。録画開始が予定より約3秒遅れたため、KonomiTV上では「一部のみ録画」と表示されました。長時間運転や幅広い環境での互換性は未確認です。

## 主な制限

- 同時録画は一件まで
- 予約の変更、削除、番組時間変更への追従は未対応
- 録画中の再接続や自動再試行は未対応
- KonomiTVへ録画済み番組の一覧やファイルを返す機能は未対応
- Komorebiは未検証
- WebUIから予約、番組情報の更新、録画開始はできない
- LAN公開、ユーザー認証、TLS終端は未対応
- チューナーやDVBデバイスを直接制御しない

## 必要なもの

- Go 1.26.5
- Mirakurunまたはmirakc
- 所有者だけが読み書きできるデータ保存先
- 所有者だけが読み書きできる録画保存先

## ビルド

```console
go build -o ./bin/sazanami-dvr ./cmd/sazanami-dvr
./bin/sazanami-dvr --version
```

## 最短の起動手順

まずDBを作成し、番組情報を一度取得します。`catalog sync`を実行したときだけMirakurunへ接続します。

```console
./bin/sazanami-dvr db migrate --data-root <data-root>

./bin/sazanami-dvr catalog sync \
  --data-root <data-root> \
  --provider mirakurun \
  --base-url <mirakurun-url>
```

接続先の指定方法と失敗時の動きは[Mirakurunから番組情報を取得する](docs/mirakurun-catalog-sync.md)を参照してください。

次にチャンネル設定を用意し、保存済みカタログと一致するか確認します。設定形式は[CtrlCmdチャンネル待受の使い方](docs/ctrlcmd-channel-runtime.md)を参照してください。

```console
./bin/sazanami-dvr ctrlcmd validate \
  --data-root <data-root> \
  --channel-map <channel-map>
```

録画保存先を作り、KonomiTV向けの待受と録画処理を開始します。

```console
mkdir -m 700 <recording-root>

./bin/sazanami-dvr recording serve \
  --data-root <data-root> \
  --recording-root <recording-root> \
  --channel-map <channel-map> \
  --provider mirakurun \
  --base-url <mirakurun-url> \
  --listen 127.0.0.1:4510
```

このプロセスは、起動しただけではMirakurunへ接続しません。保存済み予約の開始時刻になったときだけ放送ストリームを開きます。停止には`SIGINT`または`SIGTERM`を使います。詳しくは[録画機能の運用手順](docs/recording-operations.md)を参照してください。

KonomiTV側の設定と、画面から一件確認する手順は
[KonomiTVと接続する](docs/konomitv-setup.md)を参照してください。

## その他のコマンド

保存済みの情報をWebUIで確認します。

```console
sazanami-dvr ui serve --data-root <data-root>
```

既定のURLは`http://127.0.0.1:40772/`です。WebUIは表示と手動バックアップだけを行います。詳しくは[運用WebUIの利用手順](docs/web-ui-operations.md)を参照してください。

DBの状態確認、移行、バックアップ、復元には次のコマンドを使います。

```console
sazanami-dvr db status  --data-root <data-root>
sazanami-dvr db migrate --data-root <data-root>
sazanami-dvr db backup  --data-root <data-root>
sazanami-dvr db restore --data-root <data-root> --backup-id <uuid>
sazanami-dvr db recover --data-root <data-root> --operation-id <uuid>
```

詳しくは[カタログDBの運用・復旧手順](docs/catalog-database-operations.md)を参照してください。

## 録画ファイル

- 番組名や放送局名はファイル名に使わない
- 録画中は`.ts.partial`、正常に完了した場合だけ`.ts`にする
- 既存の完成ファイルを上書きしない
- 188バイト未満のデータを正常な録画として扱わない
- 途中ファイルや不明なファイルを自動削除しない
- 再起動後に途中ファイルへ追記しない

## ネットワーク

引数なしで起動した場合とDBコマンドは、待受も外部接続も始めません。

| コマンド | ネットワーク動作 |
|---|---|
| `catalog sync` | Mirakurunのサービス・番組APIへ一度接続する |
| `recording serve` | KonomiTV向けに待ち受け、予約時刻だけMirakurunの放送ストリームへ接続する |
| `ctrlcmd serve` | チャンネル確認用に待ち受ける |
| `ui serve` | 運用WebUIを表示する |

待受先は`127.0.0.1`または`::1`に限定しています。ただし認証はありません。同じPC上の信頼できない利用者やプロセスから操作される可能性がある環境では使用しないでください。

## 開発

SQLiteドライバーは`github.com/ncruces/go-sqlite3 v0.35.2`を使用し、CGOは使いません。Webフレームワーク、ORM、Node.js、コード生成も使っていません。

```console
go mod verify
go test ./...
go test -shuffle=on ./...
go test -race ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
CGO_ENABLED=0 go build ./cmd/sazanami-dvr
```

Pull Requestと`main`へのpushでは、GitHub Actionsがこれらの検査と、Linux／macOSのIntel／Arm向けビルドを実行します。同じPull Requestへ続けてpushした場合は、古い実行を中止して新しいcommitだけを検査します。

主なパッケージ構成は次のとおりです。

```text
cmd/sazanami-dvr/              コマンド引数を読み、必要な機能を組み立てる
internal/core/                 予約や録画の型とルール
internal/app/                  番組同期、予約、録画、復旧の手順
internal/adapters/ctrlcmd/     KonomiTV向けCtrlCmd形式との変換
internal/adapters/provider/    Mirakurun接続とテスト用の接続処理
internal/adapters/recordingfs/ 録画ファイルの作成と完成処理
internal/adapters/sqlite/      SQLiteへの保存、バックアップ、復元
internal/adapters/webui/       ローカル専用の運用画面
```

変更方法は[CONTRIBUTING.md](CONTRIBUTING.md)、脆弱性の連絡方法は[SECURITY.md](SECURITY.md)、外部ライブラリの情報は[依存関係の記録](docs/dependencies.md)を参照してください。

## ライセンス

[MIT License](LICENSE)
