[README](../../README.md) / [ドキュメント](../README.md) / KonomiTV

# KonomiTVと接続する

Sazanami DVRを、KonomiTV v0.14.1のEDCBバックエンドとして接続します。

初回は「前提」から「一件確認する」まで進め、接続後は必要な章だけ参照してください。

## 目次

- [前提](#前提)
- [接続の流れ](#接続の流れ)
- [KonomiTVの設定](#konomitvの設定)
- [一件確認する](#一件確認する)
- [別のホストで動かす場合](#別のホストで動かす場合)
- [次に読むページ](#次に読むページ)

## 前提

次の準備を終えてください。

- Sazanami DVRが起動している
- Mirakurunまたはmirakcへ接続できる
- チャンネル設定と番組表を準備している
- KonomiTV v0.14.1を使用する
- KonomiTVから録画保存先を読み取れる

チャンネル設定と番組表の準備は、[チャンネルと番組表を準備する](../guides/channels-and-epg.md)を参照してください。
Sazanami DVRの起動方法は、[Linuxに導入する](linux.md)または[Docker Composeで起動する](docker-compose.md)を参照してください。

## 接続の流れ

通常は次の構成で動かします。

```text
KonomiTV
  ├─ 番組表・予約 ──> Sazanami DVR
  ├─ ライブ視聴 ────> Sazanami DVR
  └─ 録画済み番組 ──> 録画保存先

Sazanami DVR ──番組情報・放送ストリーム──> Mirakurun / mirakc
```

Sazanami DVRは、番組表の更新や録画、ライブ視聴が必要になったときだけMirakurunまたはmirakcへ接続します。

## KonomiTVの設定

KonomiTVの`config.yaml`で、バックエンドと接続先を設定します。

既存の設定は残したまま、次の項目を実際の値へ置き換えてください。

```yaml
general:
    backend: 'EDCB'
    always_receive_tv_from_mirakurun: false
    edcb_url: 'tcp://<sazanami-host>:4520/'
    mirakurun_url: '<mirakurun-url>'

video:
    recorded_folders:
        - '<recording-root>'
```

設定項目の意味は次のとおりです。

| 項目 | 設定内容 |
|---|---|
| `backend` | `EDCB`にする |
| `edcb_url` | Sazanami DVRのCtrlCmd接続先 |
| `mirakurun_url` | MirakurunまたはmirakcのURL |
| `recorded_folders` | KonomiTVから読める録画保存先 |
| `always_receive_tv_from_mirakurun` | `false`ならライブ視聴もSazanami DVRを経由する |

`always_receive_tv_from_mirakurun: true`にすると、ライブ視聴だけMirakurunまたはmirakcへ直接接続します。番組表と録画予約は、引き続きSazanami DVRへ接続します。

## 一件確認する

1. Sazanami DVRを起動する
2. KonomiTVを起動する
3. 番組表にチャンネルと番組が表示されるまで待つ
4. 番組を一件予約する
5. KonomiTVの予約一覧に表示されることを確認する
6. 録画開始後、録画中として表示されることを確認する
7. 録画終了後、「ビデオをみる」に番組が表示されることを確認する
8. 録画済み番組を再生する

放送中の番組を途中から録画する場合や、録画中にKonomiTVから停止する場合は、[予約と録画を使う](../guides/recording.md)を参照してください。

## 別のホストで動かす場合

Sazanami DVRとKonomiTVを別のホストで動かす場合は、次の2点を確認してください。

- `<sazanami-host>`にSazanami DVRホストのLANアドレスを指定する
- 録画保存先をKonomiTVホストへ共有し、`recorded_folders`にはKonomiTVから見えるパスを指定する

KonomiTVをDockerコンテナで動かす場合、コンテナ内の`127.0.0.1`はSazanami DVRホストを指しません。Sazanami DVRホストのLANアドレスを指定してください。

Sazanami DVRのCtrlCmdには認証とTLSがありません。信頼できる宅内LANだけで使用し、インターネットへ公開しないでください。

## 次に読むページ

- [予約と録画を使う](../guides/recording.md)
- [ドキュメント一覧](../README.md)
