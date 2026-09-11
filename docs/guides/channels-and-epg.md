[README](../../README.md) / [ドキュメント](../README.md) / チャンネルと番組表

# チャンネルと番組表を準備する

Sazanami DVRは、Mirakurunまたはmirakcからサービスと番組情報を取得し、番組表として保存します。

初回は「DBを準備する」から「設定を検証する」まで進めてください。番組表の確認だけが必要なときは、最後の「番組表を更新する」と「うまくいかない場合」を参照します。

## 目次

- [前提](#前提)
- [DBを準備する](#dbを準備する)
- [番組情報を同期する](#番組情報を同期する)
- [チャンネル設定を作成する](#チャンネル設定を作成する)
- [設定を検証する](#設定を検証する)
- [番組表を更新する](#番組表を更新する)
- [うまくいかない場合](#うまくいかない場合)
- [次に読むページ](#次に読むページ)

## 前提

- Mirakurunまたはmirakcが起動している
- Sazanami DVRから接続先へ到達できる
- Sazanami DVRのデータ保存先を決めている
- データ保存先を、Sazanami DVRの実行ユーザーが読み書きできる

ここでは、データ保存先を`<data-root>`、接続先を`<mirakurun-url>`と表記します。

## DBを準備する

初回はDBの状態を確認し、必要な更新を行います。

```console
sazanami-dvr db status --data-root <data-root>
sazanami-dvr db migrate --data-root <data-root>
sazanami-dvr db status --data-root <data-root>
```

最後の出力が次の状態になっていることを確認してください。

```text
state=CURRENT
```

`CURRENT`にならない場合はサービスを起動せず、[バックアップと復元](../operations/backup-and-restore.md)を確認してください。

## 番組情報を同期する

`catalog sync`で、Mirakurun互換APIからサービス一覧と番組一覧を取得します。

```console
sazanami-dvr catalog sync \
  --data-root <data-root> \
  --provider mirakurun \
  --base-url <mirakurun-url>
```

`<mirakurun-url>`には、スキーム、ホスト、ポートを含むURLを指定します。

この操作で取得するのは、サービス一覧と番組一覧です。放送ストリームの取得や録画は行いません。

同期に失敗した場合は新しい番組表を公開せず、直前に正常取得した番組表を使い続けます。

## チャンネル設定を作成する

`<data-root>/channels.json`を作成します。次の例を実際のサービス情報へ置き換えてください。

```json
{
  "format": "sazanami-channel-map-v1",
  "backend_id": "<catalog-backend-id>",
  "services": [
    {
      "provider_locator": "<provider-locator>",
      "network_id": 32736,
      "service_id": 1024,
      "transport_stream_id": 32736,
      "provider_name": "",
      "network_name": "",
      "transport_stream_name": "",
      "remote_control_key_id": 1,
      "partial_reception": false,
      "epg_capture": true,
      "search": true
    }
  ]
}
```

設定時は次を守ってください。

- `backend_id`は`catalog sync`成功時に表示された値を使う
- `provider_locator`はMirakurun互換APIの`/api/services`にある`id`を、10進数の文字列として記載する
- `network_id`と`service_id`は、同じサービスの`networkId`と`serviceId`を使う
- `remote_control_key_id`は、同じサービスに`remoteControlKeyId`があればその値を使う
- `transport_stream_id`は放送設備またはMirakurun / mirakcの設定で確認できた値を使う
- TSIDが確認できない場合は、推測値を入力しない
- サービス数は4,096件以下にする
- ファイル全体を1MiB以下にする

チャンネル設定は、保存済み番組表と同じ接続先の情報に合わせてください。

## 設定を検証する

待受を始めずに、チャンネル設定と番組表の対応を確認します。

```console
sazanami-dvr ctrlcmd validate \
  --data-root <data-root> \
  --channel-map <data-root>/channels.json
```

成功時は、設定されたサービス数が表示されます。この検証が失敗した場合、KonomiTVを起動しても番組表や予約を正しく利用できません。チャンネル設定を修正して、もう一度実行してください。

## 番組表を更新する

KonomiTVの予約と録画を使う場合は、Sazanami DVRの`recording serve`を起動します。起動方法は、導入方法に合わせて次のページを参照してください。

- Linuxのsystemd版: [Linuxに導入する](../getting-started/linux.md)
- Docker Compose版: [Docker Composeで起動する](../getting-started/docker-compose.md)

`recording serve`は、起動直後に番組表を更新し、その後も定期的に更新します。既定の更新間隔は5分です。

番組表の更新に失敗しても、直前に正常取得した番組表と、すでに動いている録画処理は維持されます。

## うまくいかない場合

| 状況 | 確認すること |
|---|---|
| `state=CURRENT`にならない | DBを使用中のサービスを止め、[バックアップと復元](../operations/backup-and-restore.md)を確認する |
| 同期に失敗する | `<mirakurun-url>`、Mirakurunまたはmirakcの起動状態、接続元からの到達性を確認する |
| チャンネル設定の検証に失敗する | `backend_id`、サービス識別子、ONID・TSID・SIDの値を確認する |
| 番組表が空になる | `catalog sync`が成功したか、設定が同じ接続先を指しているか確認する |
| 番組表が古い | `recording serve`が起動しているか、次の定期更新を待つ |

## 次に読むページ

- [KonomiTVと接続する](../getting-started/konomitv.md)
- [ドキュメント一覧](../README.md)
