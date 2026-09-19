[README](../../README.md) / [ドキュメント](../README.md) / Linuxの導入

# Linuxに導入する

Sazanami DVRを、systemdを利用できるLinuxへインストールします。
初回導入では、インストール、Mirakurun URLの設定、自動セットアップ、サービス起動の順に進めます。

## 目次

- [前提](#前提)
- [インストール](#インストール)
- [Mirakurun URLを設定する](#mirakurun-urlを設定する)
- [チャンネルと番組表を自動準備する](#チャンネルと番組表を自動準備する)
- [サービスを起動する](#サービスを起動する)
- [次に読む](#次に読む)

## 前提

次のものを用意してください。

- systemdが動くLinux
- `sudo`を実行できるアカウント
- 利用できるMirakurunまたはmirakc

配布アーカイブの`<arch>`は、利用するCPUに合わせて`amd64`または`arm64`へ読み替えます。

## インストール

[GitHub Releases](https://github.com/g0ooo0gle/sazanami-dvr/releases)から、CPUに合う配布アーカイブをダウンロードします。`uname -m`が`x86_64`なら`amd64`、`aarch64`または`arm64`なら`arm64`を選んでください。

ダウンロードしたファイルを展開し、そのディレクトリでインストーラを実行します。

```sh
tar -xzf sazanami-dvr_<version>_linux_<arch>.tar.gz
cd sazanami-dvr_<version>_linux_<arch>
sudo ./packaging/install.sh install
```

インストーラは次のものを配置します。

- `sazanami-dvr`専用ユーザーとグループ
- `/opt/sazanami-dvr/<version>/`の実行ファイル
- `/usr/local/bin/sazanami-dvr`の実行リンク
- systemdのサービス定義
- `/etc/sazanami-dvr/sazanami-dvr.env`

インストーラは、DB、番組表、チャンネル設定を変更せず、サービスも起動しません。初回の準備は次の手順で行います。

同じ版を再インストールする場合も、先にサービスを停止してください。

```sh
sudo systemctl stop sazanami-dvr.service
sudo ./packaging/install.sh install
```

別の版へ更新する場合は、[更新・切り戻し・削除](../operations/update-and-remove.md)を参照してください。

## Mirakurun URLを設定する

環境設定を開き、MirakurunまたはmirakcのURLを設定します。

```sh
sudoedit /etc/sazanami-dvr/sazanami-dvr.env
```

通常は`SAZANAMI_MIRAKURUN_URL`だけを変更します。ここで設定したURLはサービス起動時にも使われます。

| 設定 | 初期値 | 内容 |
|---|---|---|
| `SAZANAMI_MIRAKURUN_URL` | `http://127.0.0.1:40772` | MirakurunまたはmirakcのURL |
| `SAZANAMI_DATA_ROOT` | `/var/lib/sazanami-dvr` | DBとバックアップの保存先 |
| `SAZANAMI_RECORDING_ROOT` | `/var/lib/sazanami-dvr/recordings` | 録画ファイルの保存先 |
| `SAZANAMI_CHANNEL_MAP` | `/var/lib/sazanami-dvr/channels.json` | チャンネル設定の場所 |
| `SAZANAMI_CTRLCMD_LISTEN` | `0.0.0.0:4520` | KonomiTVなどから接続する待受 |
| `SAZANAMI_RECORDING_HTTP_LISTEN` | `127.0.0.1:4521` | 録画取得用HTTPの待受 |
| `SAZANAMI_WEBUI_LISTEN` | `127.0.0.1:4522` | 手動起動するWebUIの待受 |

CtrlCmdは初期状態では`0.0.0.0:4520`で待ち受けます。認証とTLSはないため、信頼できるLAN内だけで使用してください。ルーターのポート転送は設定しないでください。

同じPCからだけ接続する場合は、待受を`127.0.0.1:4520`に変更できます。別のPCからKonomiTVなどで接続する場合は、接続元から到達できるLANアドレスを指定してください。

## チャンネルと番組表を自動準備する

サービスを起動する前に、Mirakurun URLだけを指定して初回セットアップを実行します。

```sh
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr setup \
  --mirakurun-url "$(sudo sed -n 's/^SAZANAMI_MIRAKURUN_URL=//p' /etc/sazanami-dvr/sazanami-dvr.env)" \
  --data-root /var/lib/sazanami-dvr
```

`setup`は、DBの初期化または状態確認、起動前の復旧と古い番組表の自動整理、Mirakurunからの番組表同期、
サービスの自動確認、`/var/lib/sazanami-dvr/channels.json`の生成をまとめて行います。
成功時は`result=completed`と`channel_map=created`または`channel_map=unchanged`が表示されます。

サービス確認では一時的なストリーム接続を使うため、録画やライブ視聴が動いていると空きチューナーが
足りないことがあります。その場合は録画と視聴を止めてから、時間を置いて再実行してください。

確認方法とチャンネル設定の扱いは[チャンネルと番組表](../guides/channels-and-epg.md)を参照してください。

既存の`channels.json`は上書きしません。同じ内容なら`unchanged`で成功し、内容が異なる場合は既存ファイルを
残したまま失敗します。チャンネル構成を更新する場合は、[更新・切り戻し・削除](../operations/update-and-remove.md)の
明示手順を使ってください。

DBが`BEHIND`の場合、`setup`は自動でmigrationしません。表示された状態を確認し、サービスを停止した状態で
次のコマンドを実行してから`setup`をやり直してください。

```sh
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db migrate \
  --data-root /var/lib/sazanami-dvr

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr
```

`CURRENT`にならない場合はサービスを起動せず、[バックアップと復元](../operations/backup-and-restore.md)を確認してください。

## サービスを起動する

準備が終わったら、systemdでサービスを有効化して起動します。

```sh
sudo systemctl enable --now sazanami-dvr.service
sudo systemctl status sazanami-dvr.service
```

`Active: active (running)`と表示されれば起動完了です。

環境設定を変更した場合は、サービスを再起動します。

```sh
sudo systemctl restart sazanami-dvr.service
```

WebUIは録画サービスと同時には起動できません。利用する場合は、録画がない時間に[WebUIガイド](../guides/web-ui.md)の手順で切り替えてください。

## 次に読む

- [KonomiTVと接続する](konomitv.md)
- [ドキュメント一覧](../README.md)
