[README](../../README.md) / [ドキュメント](../README.md) / ライブ視聴

# ライブ視聴を使う

KonomiTV v0.14.1から、Sazanami DVR経由でMirakurunまたはmirakcの放送を視聴できます。

Sazanami DVRは放送データを変換せず、受信したMPEG-TSをそのまま中継します。

## 前提

- Sazanami DVRが起動している
- チャンネル設定に視聴するサービスが含まれている
- KonomiTVのバックエンドが`EDCB`になっている
- KonomiTVからSazanami DVRのCtrlCmdへ接続できる
- Mirakurunまたはmirakcから対象サービスを取得できる

KonomiTVの接続設定は、[KonomiTVと接続する](../getting-started/konomitv.md)を参照してください。

## Sazanami DVR経由で視聴する

KonomiTVの設定で、次の値を使用します。

```yaml
general:
    backend: 'EDCB'
    always_receive_tv_from_mirakurun: false
    edcb_url: 'tcp://<sazanami-host>:4520/'
    mirakurun_url: '<mirakurun-url>'
```

KonomiTVでチャンネルを選ぶと、Sazanami DVRが対象サービスを確認して放送ストリームを開きます。ライブ視聴は録画予約や録画ファイルとは別の処理です。ライブ視聴を開始しても録画履歴は作成されません。

## Mirakurunへ直接接続する

`always_receive_tv_from_mirakurun: true`にすると、KonomiTVのライブ視聴だけMirakurunまたはmirakcへ直接接続します。

この設定でも、次の機能はSazanami DVRを使用します。

- 番組表
- 予約
- 録画
- 録画済み番組の参照

ライブ視聴もSazanami DVRで中継したい場合は、`false`にしてください。

## 視聴を終了する

KonomiTVの視聴画面を閉じるか、別のチャンネルへ切り替えると、現在のライブ接続を終了します。終了したライブ接続は、録画ファイルや番組表へ影響しません。

## 制限

- 同時に視聴できるライブ接続は最大4本
- チャンネル選択後、30秒以内に受信が始まらない接続は終了
- 受信データが10秒以上進まない接続は終了する場合がある
- 一つのライブ接続は最長12時間
- ライブ視聴は録画用の同時実行数を消費しない
- Mirakurunまたはmirakc側のチューナー数や優先度の制限は別に受ける
- 画質変更や再エンコードは行わない

## うまくいかない場合

| 状況 | 確認すること |
|---|---|
| ライブ画面を開けない | Sazanami DVRが起動しているか、`edcb_url`が正しいか確認する |
| チャンネルを選べない | チャンネル設定のONID、TSID、SIDが番組表と一致しているか確認する |
| 受信が始まらない | Mirakurunまたはmirakcの対象サービスとストリーム応答を確認する |
| コンテナから接続できない | `127.0.0.1`ではなくSazanami DVRホストのLANアドレスを指定する |
| 途中で止まる | Mirakurun側のチューナー数、接続状態、LANの切断を確認する |

Sazanami DVRのCtrlCmdには認証とTLSがありません。信頼できる宅内LANだけで使用してください。

## 次に読むページ

- [KonomiTVの設定を確認する](../getting-started/konomitv.md)
- [ドキュメント一覧](../README.md)
