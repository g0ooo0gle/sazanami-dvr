[README](../../README.md) / [ドキュメント](../README.md) / Docker Composeの導入

# Docker Composeで導入する

Sazanami DVRとKonomiTVを、Linux上のDocker Composeで起動します。

この構成には、Mirakurun、mirakc、チューナー、ドライバー、カードは含まれません。先にホスト側でMirakurunまたはmirakcを利用できる状態にしてください。

## 目次

- [前提](#前提)
- [Composeファイルを準備する](#composeファイルを準備する)
- [設定する](#設定する)
- [チャンネルと番組表を自動準備する](#チャンネルと番組表を自動準備する)
- [起動する](#起動する)
- [保存先と接続先](#保存先と接続先)
- [次に読む](#次に読む)

## 前提

次のものを用意してください。

- Docker Engine
- `docker compose`コマンド
- Linuxホスト
- 利用できるMirakurunまたはmirakc

同梱のCompose例は、KonomiTVを`linux/amd64`でビルドする構成です。初回ビルドでは依存パッケージを取得するため、完了まで時間がかかることがあります。

`prepare.sh`は、実行したユーザーのUID/GIDを設定します。`sudo ./prepare.sh`ではなく、通常のユーザーで実行してください。

## Composeファイルを準備する

配布アーカイブの`packaging/compose`を、設定とデータを保存するディレクトリへコピーします。

```sh
mkdir -p <install-dir>
cp -R packaging/compose/. <install-dir>/
cd <install-dir>
./prepare.sh
```

`prepare.sh`は、次の8つのディレクトリを`0700`で作成します。

- `data/sazanami`
- `data/konomitv`
- `data/konomitv/home`
- `data/konomitv/cache`
- `logs/konomitv`
- `recordings`
- `captures`
- `config`

録画ディレクトリには、管理用の`.sazanami-dvr.lock`を作成します。lockは実行ユーザー所有の通常ファイルとして扱われます。

`.env`と`config/konomitv.yaml`は、存在しない場合だけ作成されます。チャンネル設定は初回セットアップで自動作成します。

## 設定する

`.env`を開き、MirakurunまたはmirakcのURLを確認します。

```sh
vi .env
```

`SAZANAMI_IMAGE`には配布版に合うタグが入っているため、初回導入では変更しません。通常は`MIRAKURUN_URL`だけを実際の接続先へ変更します。

```ini
MIRAKURUN_URL=http://127.0.0.1:40772
```

次に、KonomiTVの設定を開きます。

```sh
vi config/konomitv.yaml
```

`general.mirakurun_url`を、`.env`の`MIRAKURUN_URL`と同じURLへ変更します。

`general.backend`は`EDCB`、`general.edcb_url`は`tcp://127.0.0.1:4520/`のまま使用します。録画とキャプチャの保存先も、同梱の初期値をそのまま使えます。
`always_receive_tv_from_mirakurun`は同梱例の`true`をそのまま使います。ライブ視聴をSazanami DVRで中継する場合だけ`false`へ変更してください。

## チャンネルと番組表を自動準備する

KonomiTVを起動する前にKonomiTVをビルドし、Mirakurun URLだけを指定してSazanami DVRの初回セットアップを実行します。

```sh
docker compose build konomitv

docker compose run --rm sazanami setup \
  --mirakurun-url "$(sed -n 's/^MIRAKURUN_URL=//p' .env)" \
  --data-root /var/lib/sazanami-dvr
```

`setup`はDBの初期化または状態確認、起動前の復旧と古い番組表の自動整理、番組表同期、PATによるTSID確認、
`data/sazanami/channels.json`の生成をまとめて行います。成功時は`result=completed`と表示されます。

PAT確認では対象サービスのストリームを短時間開くため、録画やライブ視聴が動いていると空きチューナーが
足りないことがあります。その場合は録画と視聴を止めてから、時間を置いて再実行してください。

既存の`data/sazanami/channels.json`は上書きしません。同じ内容なら成功しますが、内容が異なる場合は既存ファイルを
残したまま失敗します。チャンネル構成を更新する場合は、[更新・切り戻し・削除](../operations/update-and-remove.md)の
明示手順を使ってください。

DBが`BEHIND`の場合、`setup`は自動でmigrationしません。サービスを停止した状態で次の順に実行し、
`setup`をやり直してください。

```sh
docker compose run --rm sazanami db status --data-root /var/lib/sazanami-dvr
docker compose run --rm sazanami db migrate --data-root /var/lib/sazanami-dvr
docker compose run --rm sazanami db status --data-root /var/lib/sazanami-dvr
```

`CURRENT`にならない場合は
`docker compose up -d`へ進まず、[バックアップと復元](../operations/backup-and-restore.md)を確認してください。

## 起動する

準備が終わったら、2つのサービスを起動します。

```sh
docker compose up -d
docker compose ps
```

Sazanami DVRのヘルスチェックが成功すると、KonomiTVが起動します。

KonomiTVの画面は、ホストの次のURLで開きます。

```text
http://127.0.0.1:7200/
```

別のPCから接続する場合は、`127.0.0.1`をComposeを起動したホストのアドレスへ置き換えます。

## 保存先と接続先

標準構成では、次の場所にデータを保存します。

| 用途 | 保存先 |
|---|---|
| Sazanami DVRのDB・設定 | `data/sazanami/` |
| 録画ファイル | `recordings/` |
| KonomiTVの設定 | `config/konomitv.yaml` |
| KonomiTVのデータ | `data/konomitv/` |
| KonomiTVのログ | `logs/konomitv/` |
| キャプチャ | `captures/` |

接続先は次のとおりです。

| 用途 | 接続先 |
|---|---|
| CtrlCmd | `127.0.0.1:4520` |
| 録画HTTP | `127.0.0.1:4521` |
| KonomiTV | `127.0.0.1:7200` |

標準構成では、Sazanami DVRのコンテナは読み取り専用で起動します。録画ディレクトリもKonomiTVには読み取り専用で渡されます。

Compose構成に自動purgeはありません。停止やコンテナ削除でホスト側の設定、DB、録画、ログ、キャプチャが削除されることはありません。

KonomiTVから録画を削除する場合は、[更新・切り戻し・削除](../operations/update-and-remove.md)を参照してください。

## 次に読む

- [KonomiTVと接続する](konomitv.md)
- [ドキュメント一覧](../README.md)
