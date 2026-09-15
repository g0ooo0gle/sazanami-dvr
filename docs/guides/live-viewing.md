[README](../../README.md) / [ドキュメント](../README.md) / ライブ視聴

# ライブ視聴を使う

KonomiTV v0.14.1では、ライブ視聴をMirakurunまたはmirakcへ直接渡す構成を標準にしています。
番組表、予約、録画、録画済み番組は引き続きSazanami DVRを使います。

## 目次

- [前提](#前提)
- [標準: Mirakurunへ直接接続する](#標準-mirakurunへ直接接続する)
- [Sazanami DVR経由で視聴する](#sazanami-dvr経由で視聴する)
- [視聴を終了する](#視聴を終了する)
- [制限](#制限)
- [うまくいかない場合](#うまくいかない場合)
- [次に読むページ](#次に読むページ)

## 前提

- Sazanami DVRの`setup`が完了している
- Sazanami DVRが起動している
- KonomiTVのバックエンドが`EDCB`になっている
- KonomiTVからMirakurunまたはmirakcへ接続できる

KonomiTVの接続設定は、[KonomiTVと接続する](../getting-started/konomitv.md)を参照してください。

## 標準: Mirakurunへ直接接続する

KonomiTVの設定で、次の値を使用します。

```yaml
general:
    backend: 'EDCB'
    always_receive_tv_from_mirakurun: true
    edcb_url: 'tcp://<sazanami-host>:4520/'
    mirakurun_url: '<mirakurun-url>'
```

`always_receive_tv_from_mirakurun: true`では、KonomiTVのライブ視聴だけMirakurunまたはmirakcへ直接接続します。
番組表、予約、録画、録画済み番組の参照はSazanami DVRへ接続します。Sazanami DVRはライブの中継処理を行わないため、
チューナーの割り当てはMirakurunまたはmirakc側で行われます。

## Sazanami DVR経由で視聴する

ライブ接続もSazanami DVRで中継したい場合だけ、次のように`false`を明示します。

```yaml
general:
    backend: 'EDCB'
    always_receive_tv_from_mirakurun: false
    edcb_url: 'tcp://<sazanami-host>:4520/'
    mirakurun_url: '<mirakurun-url>'
```

この設定では、KonomiTVのライブ要求をSazanami DVRが受け、Mirakurunまたはmirakcから受信したMPEG-TSを変換せずに
中継します。ライブ視聴を開始しても録画履歴は作成されません。

## 視聴を終了する

KonomiTVの視聴画面を閉じるか、別のチャンネルへ切り替えると、現在のライブ接続を終了します。終了したライブ接続は、
録画ファイルや番組表へ影響しません。

## 制限

次の上限はSazanami DVR経由の中継に適用されます。

- 同時に視聴できるライブ接続は最大4本
- チャンネル選択後、30秒以内に受信が始まらない接続は終了
- 受信データが10秒以上進まない接続は終了する場合がある
- 一つのライブ接続は最長12時間
- Mirakurunまたはmirakc側のチューナー数や優先度の制限は別に受ける
- 画質変更や再エンコードは行わない

## うまくいかない場合

| 状況 | 確認すること |
|---|---|
| 標準のライブ画面を開けない | KonomiTVの`mirakurun_url`、Mirakurunまたはmirakcの起動状態、接続元からの到達性を確認する |
| 中継時にチャンネルを選べない | `setup`を完了し、`channels.json`と番組表が同じ接続先のものか確認する |
| 中継の受信が始まらない | Mirakurunまたはmirakcの対象サービスと空きチューナーを確認する |
| コンテナから接続できない | `127.0.0.1`ではなく接続先ホストのLANアドレスを指定する |
| 中継が途中で止まる | Mirakurun側のチューナー数、接続状態、LANの切断を確認する |

Sazanami DVRのCtrlCmdには認証とTLSがありません。信頼できる宅内LANだけで使用してください。

## 次に読むページ

- [KonomiTVの設定を確認する](../getting-started/konomitv.md)
- [ドキュメント一覧](../README.md)
