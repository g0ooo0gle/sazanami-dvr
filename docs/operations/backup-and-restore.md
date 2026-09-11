[README](../../README.md) / [ドキュメント](../README.md) / バックアップと復元

# バックアップと復元

Sazanami DVRのDBをバックアップし、必要な場合に復元する手順です。

DBのバックアップには、番組表、予約、録画履歴などのDB情報が含まれます。録画済みの`.ts`ファイルはDBとは別に保存されるため、この手順ではコピーしません。

初回は「この操作を行う前に」から「バックアップを作成する」まで読み、障害対応時は「バックアップから復元する」以降を参照してください。

## 目次

- [この操作を行う前に](#この操作を行う前に)
- [DBの状態を確認する](#dbの状態を確認する)
- [バックアップを作成する](#バックアップを作成する)
- [バックアップから復元する](#バックアップから復元する)
- [中断した復元を再開する](#中断した復元を再開する)
- [更新前の状態へ戻す](#更新前の状態へ戻す)
- [WebUIからバックアップを作成する](#webuiからバックアップを作成する)
- [次に読むページ](#次に読むページ)

## この操作を行う前に

DBを変更または復元する前に、次を確認してください。

- Sazanami DVRを停止する
- 同じDBを使うWebUIや別のプロセスも停止する
- DB操作を通常サービスと同じOSユーザーで実行する
- DBをNFSやSMBなどの共有ファイルシステムへ置かない
- DBのロックファイルを手動で削除しない

録画中やサービスを停止できない場合は、操作を後回しにしてください。

以下は標準installerで導入した場合の例です。DBとロックファイルは専用ユーザーだけが操作できるため、各コマンドを`sazanami-dvr`ユーザーで実行します。Composeの場合は、コンテナを停止してから`docker compose run --rm --no-deps sazanami`へ読み替えてください。

## DBの状態を確認する

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root <data-root>
```

通常起動や復元後の起動前には、次の状態を確認します。

```text
state=CURRENT
```

初回のDB準備は、[チャンネルと番組表を準備する](../guides/channels-and-epg.md)を参照してください。

## バックアップを作成する

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db backup \
  --data-root <data-root>
```

成功すると、バックアップIDが表示されます。

```text
backup_id=<uuid> state=complete schema=<version>
```

この`backup_id`を、復元が終わるまで控えておいてください。バックアップの保存先はSazanami DVRのデータ保存先です。バックアップファイルは自動で削除されないため、不要になったものは内容を確認してから利用者が管理してください。

バックアップ作成中は、別のDB操作や通常起動を重ねないでください。

## バックアップから復元する

Sazanami DVRを停止してから実行します。

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db restore \
  --data-root <data-root> \
  --backup-id <uuid>
```

復元が成功したら、次の表示を確認します。

```text
phase=COMMITTED
```

続けてDBの状態を確認します。

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root <data-root>
```

`state=CURRENT`になるまで、通常サービスを起動しないでください。復元前のDBや関連ファイルは、失敗時に確認できるよう自動で削除されません。

## 中断した復元を再開する

復元が途中で止まった場合は、通常サービスを起動しないでください。操作IDが表示される前に止まったときは、データ保存先に残った`restore-<uuid>.operation.json`のファイル名からIDを確認します。標準の保存先では、次のコマンドで一覧を確認できます。

```console
sudo find /var/lib/sazanami-dvr -maxdepth 1 -type f \
  -name 'restore-*.operation.json' -printf '%f\n'
```

ファイル名の`restore-`と`.operation.json`の間が操作IDです。このファイルは復旧判断に使うため、編集や削除をしないでください。確認したIDを指定して復旧します。

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db recover \
  --data-root <data-root> \
  --operation-id <uuid>
```

正常終了した場合は、次のいずれかが表示されます。

```text
phase=COMMITTED
```

または、

```text
phase=ROLLED_BACK
```

`COMMITTED`はバックアップへの復元完了、`ROLLED_BACK`は復元前のDBへの切り戻し完了です。続けて`db status`を実行し、`state=CURRENT`を確認します。

`recover`がエラー終了した場合は、同じoperationファイルの`phase`を確認してください。`ROLLED_BACK`なら切り戻し後の`db status`へ進みます。`FAILED_NEEDS_OPERATOR`なら再実行や通常起動をせず、operationファイルとエラー表示を残して原因を調べてください。`COMMITTED`、`ROLLED_BACK`、`FAILED_NEEDS_OPERATOR`のいずれかを確認するまで、新しい復元や書き込み処理を始めないでください。

## 更新前の状態へ戻す

更新前には、サービスを停止してバックアップを作成します。

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db backup \
  --data-root <data-root>
```

更新後、DBの状態が`BEHIND`の場合だけ、新しい実行ファイルでDBを更新します。

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db migrate \
  --data-root <data-root>
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root <data-root>
```

`state=CURRENT`を確認してからサービスを起動してください。

DBの形式が変わった状態から以前の版へ戻す場合は、先に更新前のバックアップを復元し、その後に以前の版の実行ファイルを起動します。新しい形式のDBを、以前の版の実行ファイルで直接開かないでください。

更新と切り戻しの詳しい手順は[更新・切り戻し・削除](update-and-remove.md)を参照してください。

## WebUIからバックアップを作成する

WebUIの「バックアップを作成」から、現在のDBのバックアップを作成できます。

WebUIからは、DBの復元、DBの更新、サービスの再起動を行いません。復元が必要な場合はWebUIを停止し、このページのコマンドを実行してください。

## 次に読むページ

- [更新・切り戻し・削除](update-and-remove.md)
- [ドキュメント一覧](../README.md)
