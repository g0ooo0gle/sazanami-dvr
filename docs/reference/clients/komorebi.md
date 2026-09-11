[README](../../../README.md) / [ドキュメント](../../README.md) / [互換性](../compatibility.md) / Komorebi

# Komorebiとの接続

Komorebi 1.1.0-beta6について、Sazanami DVRで確認できた範囲だけを案内します。Komorebi全体との互換性は保証していません。

## 接続設定

Komorebiで次の値を設定します。

| 項目 | 値 |
|---|---|
| 利用するシステム | `EDCB (EpgTimerSrv)` |
| EDCB IPアドレス | Sazanami DVRを動かすホスト |
| EDCB TCPポート | `4520` |
| EDCB HTTP/HTTPSポート | `4521` |

systemd版のCtrlCmdは、初期状態で`0.0.0.0:4520`を使います。録画済みTS、ロゴ、HTTP経由のライブを別端末から使う場合は、`/etc/sazanami-dvr/sazanami-dvr.env`の録画HTTPもLANから到達できるアドレスへ変更します。

```ini
SAZANAMI_RECORDING_HTTP_LISTEN=<private-ip>:4521
```

変更後はサービスを再起動します。

```sh
sudo systemctl restart sazanami-dvr.service
```

標準のCompose構成はCtrlCmdと録画HTTPをloopbackだけで待ち受けるため、別端末のKomorebiからは接続できません。

CtrlCmdと録画HTTPには認証とTLSがありません。信頼できるLANだけで使い、インターネットへ公開しないでください。

## 再生設定

まずは次の組み合わせを使用してください。

- 優先ソース: `EDCB (ダイレクトストリーミング)`
- 画質: `オリジナル（変換なし）`

録画映像の解像度、ビットレート、コーデックは変換しません。非直接ライブではHTTP/HLS互換の経路を使用できますが、すべての端末や実放送での動作を保証するものではありません。

## 追加のCtrlCmd

KonomiTV固定プロファイルの19個に加えて、Komorebi向けに次の2個を受け付けます。

| 用途 | 番号 |
|---|---|
| 完成録画の一覧 | `2017` |
| 完成録画の詳細 | `2024` |

録画一覧、録画詳細、元のTSの直接再生が対象です。トランスコード、キャスト、実サムネイル、実チャプター、実タイル画像は提供していません。

## うまくいかない場合

| 状況 | 確認すること |
|---|---|
| 番組表が表示されない | CtrlCmdのホストとポート、Sazanami DVRの起動状態 |
| 録画済みTSを再生できない | HTTPポート、完成録画の状態、読取権限 |
| ライブを開始できない | MirakurunのサービスID、チャンネル設定、ストリーム応答 |
| 変換エラーになる | `オリジナル（変換なし）`と直接配信を選んだか |
| 別端末から接続できない | `--http-listen`へLANから到達できるアドレスを指定したか |

共通の切り分けは[トラブルシューティング](../../troubleshooting.md)を参照してください。
