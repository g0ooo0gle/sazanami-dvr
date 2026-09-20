[README](../../README.md) / [ドキュメント](../README.md) / コマンド

# コマンド

実行ファイル名は`sazanami-dvr`です。引数なしでは待受やDB変更を始めず、使用方法を表示して終了します。

## 目次

- [DB](#db)
- [初回セットアップ](#初回セットアップ)
- [番組表](#番組表)
- [CtrlCmd](#ctrlcmd)
- [録画サービス](#録画サービス)
- [WebUI](#webui)
- [バージョン](#バージョン)

## DB

```sh
sazanami-dvr db status --data-root <data-root>
sazanami-dvr db migrate --data-root <data-root>
sazanami-dvr db backup --data-root <data-root>
sazanami-dvr db restore --data-root <data-root> --backup-id <uuid>
sazanami-dvr db recover --data-root <data-root> --operation-id <uuid>
```

| コマンド | 用途 |
|---|---|
| `db status` | DBの状態を表示 |
| `db migrate` | DBを現在の形式へ更新 |
| `db backup` | バックアップを作成 |
| `db restore` | 指定したバックアップを復元 |
| `db recover` | 中断した復元を再開または整理 |

`migrate`、`restore`、`recover`の前に、同じDBを使うサービスとWebUIを停止します。`CURRENT`以外の状態への対応は[バックアップと復元](../operations/backup-and-restore.md)を参照してください。

## 初回セットアップ

```sh
sazanami-dvr setup \
  --mirakurun-url <mirakurun-url> \
  [--data-root <absolute-data-root>]
```

MirakurunまたはmirakcのURLから、DBの準備、番組表同期、サービスの自動確認、KonomiTV向けの
`<absolute-data-root>/channels.json`生成を一度に行います。PATで確認できないサービスは、必要に応じて放送内の
サービス一覧から自動確認します。確認できないサービスがある場合は設定を保存せず終了します。`--data-root`には正規化した絶対パスを指定します。
省略すると`/var/lib/sazanami-dvr`を使います。
空のDBは初期化しますが、migrationが必要なDBは自動で変更しません。

成功時は`result=completed`と`channel_map=created`または`channel_map=unchanged`を表示します。既存のチャンネル設定と内容が
異なる場合は上書きせず失敗します。通常運転中にこのコマンドが自動実行されることはありません。

## 番組表

```sh
sazanami-dvr catalog sync \
  --data-root <data-root> \
  --provider mirakurun \
  --base-url <mirakurun-url>
```

Mirakurunまたはmirakcからサービスと番組を取得し、DBへ保存します。同期に失敗した場合は、直前に成功した番組表を保持します。
`catalog sync`は`channels.json`を更新しません。番組表の更新前には古いcatalogの自動整理を行います。

## CtrlCmd

チャンネル設定だけを検証する場合:

```sh
sazanami-dvr ctrlcmd validate \
  --data-root <data-root> \
  --channel-map <channel-map>
```

録画機能を使わず、基本CtrlCmdだけを待ち受ける場合:

```sh
sazanami-dvr ctrlcmd serve \
  --data-root <data-root> \
  --channel-map <channel-map> \
  --listen 0.0.0.0:4520
```

予約や録画を使う場合は`recording serve`を使用します。同じポートで両方を起動しないでください。

## 録画サービス

```sh
sazanami-dvr recording serve \
  --data-root <data-root> \
  --recording-root <recording-root> \
  --channel-map <channel-map> \
  --provider mirakurun \
  --base-url <mirakurun-url>
```

番組表の更新、予約、録画、録画・再生HTTP、KonomiTV向けCtrlCmdをまとめて起動します。任意引数と既定値は[設定リファレンス](configuration.md#録画サービス)にあります。

## WebUI

```sh
sazanami-dvr ui serve \
  --data-root <data-root> \
  --listen 127.0.0.1:4522
```

WebUIはloopbackだけで待ち受けます。認証やTLS終端は提供しません。

## バージョン

次の3形式を使用できます。

```sh
sazanami-dvr version
sazanami-dvr --version
sazanami-dvr -version
```
