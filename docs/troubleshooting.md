[README](../README.md) / [ドキュメント](README.md) / トラブルシューティング

# トラブルシューティング

## 目次

- [接続できない](#接続できない)
- [初回セットアップに失敗する](#初回セットアップに失敗する)
- [DBエラーが出る](#dbエラーが出る)
- [番組表が空](#番組表が空)
- [録画が始まらない](#録画が始まらない)
- [録画中に停止できない](#録画中に停止できない)
- [録画済み一覧に表示されない](#録画済み一覧に表示されない)
- [NAS上の録画をHTTPで再生できない](#nas上の録画をhttpで再生できない)
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

## 初回セットアップに失敗する

初回は、MirakurunまたはmirakcのURLだけを指定して`setup`を実行します。

```sh
sazanami-dvr setup \
  --mirakurun-url <mirakurun-url> \
  --data-root <absolute-data-root>
```

`<absolute-data-root>`には、`..`や不要な末尾の`/`を含まない絶対パスを指定します。

表示された固定理由に応じて、次を確認してください。

| 状況 | 対応 |
|---|---|
| URLまたは接続エラー | URLのスキーム、ホスト、ポート、Mirakurunまたはmirakcの起動状態を確認する |
| PATまたは空きチューナーのエラー | 録画とライブ視聴を止め、時間を置いて再実行する。PATを確認できない対象では、放送内のサービス一覧による自動確認も行われます |
| `BEHIND`で止まる | サービスを停止し、`db status`で確認してから必要な場合だけ`db migrate`を実行する |
| 既存のチャンネル設定と異なる | 既存`channels.json`は保護されている。更新手順で別名へ退避してから再実行する |
| サービスが0件 | Mirakurunまたはmirakcのサービス設定と、製品の対応範囲を確認する |

`setup`に失敗しても、既存の`channels.json`は上書きされません。録画中や視聴中に実行せず、処理が終わってから再実行してください。

## DBエラーが出る

```sh
sazanami-dvr db status --data-root <data-root>
```

通常起動できるのは`state=CURRENT`のDBです。

| 状態 | 対応 |
|---|---|
| `EMPTY` | 初回は`setup`を実行する。手動で準備する場合は`db migrate`を実行する |
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

## NAS上の録画をHTTPで再生できない

NASから直接は読めるのに、Sazanami DVRの録画HTTPが404や途中切断になる場合は、
完成した`.ts`に別のハードリンクが残っていないか確認してください。
v1.3.3以前では、NASのごみ箱に残った`.ts.partial`が同じファイルを指すことがありました。

v1.3.4からは、一時名を完成名へ上書きせずに移動し、この余分なリンクを作りません。
保存先がこの移動方法に対応していない場合、確定処理は失敗し、保存済みデータを残します。

更新しても、既存録画やNASのごみ箱は自動で変更しません。以前の録画が該当する場合は、
録画を止めてバックアップを確認し、NAS管理者と対象のリンクを確認してください。
ごみ箱全体を削除する必要はありません。

## ライブ視聴ができない

標準のライブ視聴はMirakurunまたはmirakcへ直接接続します。KonomiTVで次の設定を確認してください。

```yaml
general:
    always_receive_tv_from_mirakurun: true
```

`true`が標準で、ライブ視聴だけMirakurunまたはmirakcへ直接接続します。Sazanami DVR経由の中継を使う場合だけ`false`を明示してください。

直接接続できない場合はKonomiTVの`mirakurun_url`とMirakurunまたはmirakcへの到達性を確認してください。中継を選んでいる場合は、
`setup`の完了、`channels.json`のNetwork ID、TSID、Service IDと、Mirakurunまたはmirakcのストリーム応答を確認してください。

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
