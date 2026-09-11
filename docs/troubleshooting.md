[README](../README.md) / [ドキュメント](README.md) / トラブルシューティング

# トラブルシューティング

## 目次

- [接続できない](#接続できない)
- [DBエラーが出る](#dbエラーが出る)
- [番組表が空](#番組表が空)
- [録画が始まらない](#録画が始まらない)
- [録画中に停止できない](#録画中に停止できない)
- [録画済み一覧に表示されない](#録画済み一覧に表示されない)
- [ライブ視聴ができない](#ライブ視聴ができない)
- [WebUIが開かない](#webuiが開かない)
- [問い合わせ時の注意](#問い合わせ時の注意)

## 接続できない

1. Sazanami DVRが起動しているか確認する
2. 接続先のホストとポートを確認する
3. CtrlCmdは`4520`、録画HTTPは`4521`か確認する
4. 別端末から使う場合はファイアウォールを確認する

systemdで動かしている場合は、次のコマンドで状態を確認できます。

```sh
systemctl status sazanami-dvr.service
ss -ltn
```

CtrlCmdには認証とTLSがありません。インターネットへ公開しないでください。

## DBエラーが出る

```sh
sazanami-dvr db status --data-root <data-root>
```

通常起動できるのは`state=CURRENT`のDBです。

| 状態 | 対応 |
|---|---|
| `EMPTY` | `db migrate`を実行する |
| `BEHIND` | `db migrate`を実行する |
| `FUTURE` | DB形式に対応する新しい実行ファイルを確認する |
| `DRIFTED`、`GAPPED` | DBを変更せず、バックアップを確認する |
| `UNREADABLE` | DB、保存先、アクセス権を確認する |

復元や移行の前には、同じDBを使うサービスとWebUIを停止してください。詳しい手順は[バックアップと復元](operations/backup-and-restore.md)にあります。

## 番組表が空

```sh
sazanami-dvr catalog sync \
  --data-root <data-root> \
  --provider mirakurun \
  --base-url <mirakurun-url>
```

あわせて次を確認します。

- Sazanami DVRからMirakurunまたはmirakcへ接続できるか
- `catalog sync`が成功しているか
- DBが`CURRENT`か
- `channels.json`のサービスIDが保存済みの番組表にあるか

設定方法は[チャンネルと番組表](guides/channels-and-epg.md)を参照してください。

## 録画が始まらない

- 予約が有効か
- 番組の予定時刻が正しいか
- 同時録画数の上限に達していないか
- Mirakurunまたはmirakcが対象サービスのストリームを返しているか
- 放送中に追加した予約なら、作成から5分以内で、録画余白を反映した終了予定まで60秒以上あるか

途中録画では開始前の映像を保存できません。詳しくは[予約と録画](guides/recording.md)を参照してください。

## 録画中に停止できない

録画開始前の予約は、KonomiTVから取り消せます。録画開始後に同じ操作を行うと、その録画だけを停止します。

すでに完成処理へ入った録画や、終了済みの録画は停止しません。188バイト以上を保存し、ファイルとDBの確定処理に成功した場合は、部分録画として公開します。

## 録画済み一覧に表示されない

- 録画処理が完了、または利用者停止で確定しているか
- 録画ファイルが保存先にあるか
- KonomiTVから録画フォルダーを読み取れるか
- DBの録画履歴が`MISSING`や`MISMATCHED`になっていないか

## ライブ視聴ができない

Sazanami DVR経由で視聴する場合は、KonomiTVで次の設定を使います。

```yaml
general:
    always_receive_tv_from_mirakurun: false
```

`true`にすると、ライブ視聴だけMirakurunまたはmirakcへ直接接続します。

Sazanami DVR経由で視聴できない場合は、`channels.json`のNetwork ID、TSID、Service IDと、Mirakurunまたはmirakcのストリーム応答を確認してください。

## WebUIが開かない

WebUIは手動で起動します。

```sh
sazanami-dvr ui serve \
  --data-root <data-root> \
  --listen 127.0.0.1:4522
```

WebUIはloopback限定のため、別端末から直接開けません。DBが`CURRENT`であることも確認してください。

## 問い合わせ時の注意

公開Issueやログへ、次の情報を貼り付けないでください。

- 家庭内のIPアドレスやホスト名
- 認証情報
- 実際の番組情報
- 録画TS
- カードや鍵の情報

脆弱性は公開Issueではなく、[SECURITY.md](../SECURITY.md)の手順で報告してください。
