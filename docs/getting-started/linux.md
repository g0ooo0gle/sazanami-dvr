[README](../../README.md) / [ドキュメント](../README.md) / Linuxの導入

# Linuxに導入する

Sazanami DVRを、systemdを利用できるLinuxへインストールします。
初回導入では、インストール、接続先の設定、番組表の準備、サービス起動の順に進めます。

## 目次

- [前提](#前提)
- [インストール](#インストール)
- [接続先とチャンネルを設定する](#接続先とチャンネルを設定する)
- [DBと番組表を準備する](#dbと番組表を準備する)
- [サービスを起動する](#サービスを起動する)
- [次に読む](#次に読む)

## 前提

次のものを用意してください。

- systemdが動くLinux
- `sudo`を実行できるアカウント
- 利用できるMirakurunまたはmirakc
- チャンネル設定ファイル`channels.json`

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

インストーラは、DBの更新、番組表の取得、チャンネル設定の配置、サービスの起動は行いません。これらは次の手順で実行します。

同じ版を再インストールする場合も、先にサービスを停止してください。

```sh
sudo systemctl stop sazanami-dvr.service
sudo ./packaging/install.sh install
```

別の版へ更新する場合は、[更新・切り戻し・削除](../operations/update-and-remove.md)を参照してください。

## 接続先とチャンネルを設定する

環境設定を開き、MirakurunまたはmirakcのURLを設定します。

```sh
sudoedit /etc/sazanami-dvr/sazanami-dvr.env
```

通常は`SAZANAMI_MIRAKURUN_URL`だけを変更します。

| 設定 | 初期値 | 内容 |
|---|---|---|
| `SAZANAMI_MIRAKURUN_URL` | `http://127.0.0.1:40772` | MirakurunまたはmirakcのURL |
| `SAZANAMI_DATA_ROOT` | `/var/lib/sazanami-dvr` | DBとバックアップの保存先 |
| `SAZANAMI_RECORDING_ROOT` | `/var/lib/sazanami-dvr/recordings` | 録画ファイルの保存先 |
| `SAZANAMI_CHANNEL_MAP` | `/var/lib/sazanami-dvr/channels.json` | チャンネル設定の場所 |
| `SAZANAMI_CTRLCMD_LISTEN` | `0.0.0.0:4520` | KonomiTVなどから接続する待受 |
| `SAZANAMI_RECORDING_HTTP_LISTEN` | `127.0.0.1:4521` | 録画取得用HTTPの待受 |
| `SAZANAMI_WEBUI_LISTEN` | `127.0.0.1:4522` | 手動起動するWebUIの待受 |

チャンネル設定をデータディレクトリへ配置します。

```sh
sudo install -o root -g sazanami-dvr -m 0640 \
  ./channels.json \
  /var/lib/sazanami-dvr/channels.json
```

CtrlCmdは初期状態では`0.0.0.0:4520`で待ち受けます。認証とTLSはないため、信頼できるLAN内だけで使用してください。ルーターのポート転送は設定しないでください。

同じPCからだけ接続する場合は、待受を`127.0.0.1:4520`に変更できます。別のPCからKonomiTVなどで接続する場合は、接続元から到達できるLANアドレスを指定してください。

## DBと番組表を準備する

サービスを起動する前に、DBの状態を確認してから更新します。

```sh
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db migrate \
  --data-root /var/lib/sazanami-dvr

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr
```

最後の表示が`state=CURRENT`になっていることを確認してください。

次に、Mirakurunまたはmirakcから番組表を取得します。`<mirakurun-url>`には、環境設定と同じURLを指定します。

```sh
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr catalog sync \
  --data-root /var/lib/sazanami-dvr \
  --provider mirakurun \
  --base-url <mirakurun-url>
```

最後にチャンネル設定を確認します。

```sh
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr ctrlcmd validate \
  --data-root /var/lib/sazanami-dvr \
  --channel-map /var/lib/sazanami-dvr/channels.json
```

`db status`が`CURRENT`にならない場合は、サービスを起動せず、表示された理由を確認してください。

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
