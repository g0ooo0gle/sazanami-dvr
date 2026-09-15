[README](../../README.md) / [ドキュメント](../README.md) / 設定

# 設定

起動方法ごとの設定場所と、主な既定値をまとめます。

## 目次

- [systemd](#systemd)
- [初回セットアップ](#初回セットアップ)
- [録画サービス](#録画サービス)
- [Docker Compose](#docker-compose)
- [KonomiTV](#konomitv)

## systemd

インストーラで導入した環境では、`/etc/sazanami-dvr/sazanami-dvr.env`を編集します。

| 変数 | 初期値 | 用途 |
|---|---|---|
| `SAZANAMI_DATA_ROOT` | `/var/lib/sazanami-dvr` | DBと運用データ |
| `SAZANAMI_RECORDING_ROOT` | `/var/lib/sazanami-dvr/recordings` | 録画ファイル |
| `SAZANAMI_CHANNEL_MAP` | `/var/lib/sazanami-dvr/channels.json` | `setup`が生成するチャンネル設定 |
| `SAZANAMI_MIRAKURUN_URL` | `http://127.0.0.1:40772` | MirakurunまたはmirakcのURL |
| `SAZANAMI_CTRLCMD_LISTEN` | `0.0.0.0:4520` | CtrlCmdの待受 |
| `SAZANAMI_RECORDING_HTTP_LISTEN` | `127.0.0.1:4521` | 録画・再生HTTPの待受 |
| `SAZANAMI_WEBUI_LISTEN` | `127.0.0.1:4522` | WebUIの待受例 |

systemdサービスが起動するのは録画サービスです。`SAZANAMI_WEBUI_LISTEN`は設定例に含まれますが、このサービスからは使いません。WebUIは[利用ガイド](../guides/web-ui.md)に従って別に起動します。

標準構成では`SAZANAMI_CHANNEL_MAP`を変更しません。`setup`の出力先は常に
`<data-root>/channels.json`であり、別の場所を指定して生成する機能はありません。

CtrlCmdには認証とTLSがありません。信頼できるLANだけで使い、インターネットへ公開しないでください。

## 初回セットアップ

`setup`は、MirakurunまたはmirakcのURLから番組表とKonomiTV向け`channels.json`を準備します。
初回導入では、次のコマンドだけを実行してください。

```sh
sazanami-dvr setup \
  --mirakurun-url <mirakurun-url> \
  --data-root /var/lib/sazanami-dvr
```

`--data-root`の既定値は`/var/lib/sazanami-dvr`です。DBが空の場合は初期化します。`CURRENT`のDBはそのまま使い、
`BEHIND`などmigrationが必要な状態は自動で変更しません。サービスを停止して`db migrate`を実行してから、
`setup`をやり直してください。

`setup`はサービスごとに短時間のPAT確認を行います。録画やライブ視聴が動いている場合は、空きチューナーが足りず
失敗することがあります。既存の`channels.json`は上書きせず、同じ内容なら`unchanged`、異なる内容なら失敗として残します。

## 録画サービス

`sazanami-dvr recording serve`の主な引数は次のとおりです。

| 引数 | 省略時 | 内容 |
|---|---|---|
| `--data-root` | — | 必須。DBと運用データの保存先 |
| `--recording-root` | — | 必須。録画ファイルの保存先 |
| `--channel-map` | — | 必須。`channels.json`の場所 |
| `--provider` | — | 必須。`mirakurun`を指定 |
| `--base-url` | — | 必須。MirakurunまたはmirakcのURL |
| `--listen` | `0.0.0.0:4520` | CtrlCmdの待受 |
| `--http-listen` | `127.0.0.1:4521` | 録画・再生HTTPの待受 |
| `--catalog-refresh-interval` | `5m` | 番組表の更新間隔 |
| `--max-concurrent-recordings` | 自動 | Mirakurunのチューナー数を使用。取得できない場合は1 |
| `--active-follow-extension-only` | 無効 | 録画開始後の番組追従を終了延長だけに限定 |
| `--post-recording-script-root` | `<data-root>/post-recording-scripts` | 録画後スクリプトの保存先 |

番組表の更新間隔は5分以上24時間以下です。同時録画数を明示しない場合は、起動時にMirakurunのチューナー一覧を一度取得します。

## Docker Compose

標準Composeは、Sazanami DVRとKonomiTVを同じLinuxホストで動かします。主な環境変数は次のとおりです。

| 変数 | 用途 |
|---|---|
| `HOST_UID`、`HOST_GID` | ホスト側の実行ユーザー |
| `TZ` | タイムゾーン |
| `SAZANAMI_IMAGE` | Sazanami DVRのイメージ |
| `KONOMITV_IMAGE` | KonomiTVのイメージ |
| `KONOMITV_BUILD_CONTEXT` | KonomiTVのビルド元 |
| `MIRAKURUN_URL` | MirakurunまたはmirakcのURL |
| `SAZANAMI_DATA_DIR` | Sazanami DVRのデータ保存先 |
| `RECORDING_DIR` | 録画ファイルの保存先 |
| `KONOMITV_CONFIG_FILE` | KonomiTVの設定ファイル |
| `KONOMITV_DATA_DIR` | KonomiTVのデータ保存先 |
| `KONOMITV_LOG_DIR` | KonomiTVのログ保存先 |
| `CAPTURE_DIR` | キャプチャ保存先 |

配布アーカイブの`.env.example`には、そのリリースに合うイメージタグとKonomiTVの固定リビジョンが入っています。初回は`prepare.sh`で`.env`を作り、必要なパスとMirakurunのURLを確認してください。

詳しい手順は[Docker Composeで導入する](../getting-started/docker-compose.md)を参照してください。

## KonomiTV

主な接続設定は次のとおりです。

```yaml
general:
    backend: 'EDCB'
    always_receive_tv_from_mirakurun: true
    edcb_url: 'tcp://127.0.0.1:4520/'
    mirakurun_url: 'http://127.0.0.1:40772/'

server:
    port: 7200

video:
    recorded_folders:
        - '/recordings'
```

ホストを分ける場合や録画先を変更する場合は、[KonomiTVと接続する](../getting-started/konomitv.md)を参照してください。

`always_receive_tv_from_mirakurun: true`が標準です。ライブ視聴だけをMirakurunまたはmirakcへ直接接続します。
Sazanami DVRでライブを中継する場合だけ`false`を明示してください。

セキュリティ上の注意は[SECURITY.md](../../SECURITY.md)にあります。
