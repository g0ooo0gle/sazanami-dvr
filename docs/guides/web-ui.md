[README](../../README.md) / [ドキュメント](../README.md) / WebUI

# WebUIを使う

WebUIでは、Sazanami DVRが保存している番組情報、DBの状態、ビルド情報を同じPCのブラウザーから確認できます。

WebUIを開いても、番組情報の取得、チューナー操作、録画は始まりません。初回は「前提」から「WebUIを起動する」まで進め、以降は必要な項目だけ参照してください。

## 前提

- Sazanami DVRの実行ファイルを使用できる
- データ保存先を指定できる
- DBの状態が`CURRENT`である

DBがまだ準備できていない場合は、[チャンネルと番組表を準備する](channels-and-epg.md)を参照してください。

WebUIは、同じデータ保存先を使う録画サービスと同時には起動できません。録画がない時間にサービスを停止して使用してください。

## WebUIを起動する

systemd版では、先に録画サービスを停止します。

```sh
sudo systemctl stop sazanami-dvr.service
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr ui serve \
  --data-root /var/lib/sazanami-dvr
```

Compose版では、Composeファイルのあるディレクトリで次を実行します。

```sh
docker compose down
docker compose run --rm --no-deps sazanami ui serve \
  --data-root /var/lib/sazanami-dvr
```

初期状態では、次のURLで待ち受けます。

```text
http://127.0.0.1:4522/
```

直接起動している環境では、そのプロセスを停止してから`sazanami-dvr ui serve --data-root <data-root>`を実行します。別のloopbackポートを使う場合は、`--listen`を指定します。

```console
sazanami-dvr ui serve \
  --data-root <data-root> \
  --listen 127.0.0.1:40800
```

ブラウザーで指定したURLを開いてください。WebUIの待受先には、`127.0.0.1`または`::1`だけを指定できます。LAN向けアドレスや`localhost`は使用できません。

停止するには、起動中のプロセスへ`SIGINT`または`SIGTERM`を送ります。その後、通常の録画サービスを戻します。

```sh
# systemd版
sudo systemctl start sazanami-dvr.service

# Compose版
docker compose up -d
```

## 画面で確認できること

WebUIでは、次の情報を確認できます。

- DBの状態
- 番組情報の取得元
- Sazanami DVRのビルド情報
- 保存済みの番組表
- 現在の処理上限
- オンラインバックアップの操作

番組時刻は日本時間で表示されます。番組表は保存済みのデータから現在時刻付近を表示します。WebUIを開いただけではMirakurunまたはmirakcへ接続しません。

## WebUIからバックアップを作成する

画面の「バックアップを作成」を押すと、現在のDBのバックアップを一件作成できます。

復元、DBの更新、サービスの再起動はWebUIから行いません。復元が必要な場合はWebUIを停止し、[バックアップと復元](../operations/backup-and-restore.md)の手順を使ってください。

## うまくいかない場合

| 表示 | 確認すること |
|---|---|
| `current-database-required` | `db status`を実行し、DBが`CURRENT`か確認する |
| `database-owner-unavailable` | 同じデータ保存先を使用しているプロセスを停止する |
| `loopback-listen-required` | `127.0.0.1`または`::1`を指定する |
| `loopback-listen-failed` | 指定したポートを別のプロセスが使っていないか確認する |
| `backup-busy` | 先に開始したバックアップが終わるまで待つ |

WebUIには認証とTLSがありません。LANやインターネットへ公開しないでください。ロックファイルを手動で削除しないでください。

## 次に読むページ

- [バックアップと復元](../operations/backup-and-restore.md)
- [ドキュメント一覧](../README.md)
