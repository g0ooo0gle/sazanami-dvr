[README](../../README.md) / [ドキュメント](../README.md) / チャンネルと番組表

# チャンネルと番組表を準備する

初回はMirakurunまたはmirakcのURLだけで`setup`を実行します。サービス情報、TSID、番組表、
KonomiTV向けの`channels.json`をまとめて準備できます。

## 目次

- [前提](#前提)
- [初回セットアップ](#初回セットアップ)
- [番組表を更新する](#番組表を更新する)
- [番組表DBの自動整理](#番組表dbの自動整理)
- [チャンネル構成を更新する](#チャンネル構成を更新する)
- [うまくいかない場合](#うまくいかない場合)
- [次に読むページ](#次に読むページ)

## 前提

- Mirakurunまたはmirakcが起動している
- Sazanami DVRから接続先へ到達できる
- Sazanami DVRのデータ保存先を決めている
- データ保存先を、Sazanami DVRの実行ユーザーが読み書きできる

ここでは、データ保存先を`<absolute-data-root>`、接続先を`<mirakurun-url>`と表記します。
データ保存先には、`..`や不要な末尾の`/`を含まない絶対パスを指定してください。

## 初回セットアップ

サービスを起動する前に、次のコマンドを一度実行します。

```console
sazanami-dvr setup \
  --mirakurun-url <mirakurun-url> \
  --data-root <absolute-data-root>
```

`--data-root`を省略すると`/var/lib/sazanami-dvr`を使います。`setup`は次の処理を一つの操作として行います。

- DBが空なら初期化し、既存DBなら状態を確認する
- 起動前の復旧と番組表DBの自動整理を行う
- Mirakurun互換APIからサービスと番組表を同期する
- 対応する放送サービスのストリームを短時間読み、PATからTSIDを確認する
- 最新の番組表と照合した`<absolute-data-root>/channels.json`を作成する

成功時は`result=completed`と、`channel_map=created`または`channel_map=unchanged`が表示されます。
このファイルはKonomiTV用の静的なチャンネル一覧です。通常の番組表更新で書き換えられることはありません。

PAT確認ではサービスごとに一時的なストリーム接続を使います。録画やライブ視聴が動いていると空きチューナーが
足りないことがあるため、停止してから再実行してください。録画TSやPATの内容は保存しません。

`setup`は既存の`channels.json`を上書きしません。同じ内容ならそのまま成功し、異なる内容なら既存ファイルを
残したまま失敗します。チャンネル構成を変える場合は、[チャンネル構成を更新する](#チャンネル構成を更新する)の
手順を使ってください。

DBが`BEHIND`の場合は自動でmigrationしません。サービスを停止して状態を確認し、必要な場合だけ次のコマンドを
実行してから`setup`をやり直します。

```console
sazanami-dvr db status --data-root <absolute-data-root>
sazanami-dvr db migrate --data-root <absolute-data-root>
sazanami-dvr db status --data-root <absolute-data-root>
```

`CURRENT`にならない場合はサービスを起動せず、[バックアップと復元](../operations/backup-and-restore.md)を確認してください。

## 番組表を更新する

初回セットアップ後の番組表更新は、通常は`recording serve`が自動で行います。明示的に更新する場合は、次の
コマンドを使います。

```console
sazanami-dvr catalog sync \
  --data-root <absolute-data-root> \
  --provider mirakurun \
  --base-url <mirakurun-url>
```

`catalog sync`はサービス一覧と番組一覧を更新しますが、`channels.json`は書き換えません。同期に失敗した場合は
新しい番組表を公開せず、直前に正常取得した番組表を使い続けます。

Mirakurunのサービス一覧にないチャンネルの番組は、未解決として記録して同期を続けます。
その番組は番組表や新規録画候補に出ず、サービスを確認できた次回の同期から通常どおり扱います。
既存の予約や録画履歴は削除しません。

番組名・説明・詳細欄・放送局名に「�」（U+FFFD）があっても、その文字を保持して同期を続けます。
文字列の不正な文字コードが「�」に置き換わった場合も同様です。元の文字の復元は行いません。
IDやJSONの構造が不正な場合は、従来どおり同期を失敗として扱います。

## 番組表DBの自動整理

`recording serve`と`catalog sync`は、新しい番組表を作る前に自動で古いデータを整理します。手動のGC設定や全消去は
必要ありません。

完了済みの番組表世代はバックエンドごとに最新3件を残し、世代の要約、番組、サービスは30日を基本の保持期間とします。
録画や予約から参照されている情報と、処理中の世代は削除しません。1回の削除は1,000行以下、1回の整理は30秒以内に
制限されます。時間内に終わらなかった場合は、次回の更新で続きから整理します。

整理で空いたSQLiteファイルの領域は、すぐにファイルサイズへ反映されないことがあります。その後の更新で再利用されるため、
ファイルが縮まらなくても番組表が増え続けているとは限りません。録画ファイル、予約、録画履歴はこの整理の対象外です。

## チャンネル構成を更新する

Mirakurun側でサービスを追加・削除した場合も、通常運転中に自動追従はしません。録画やライブ視聴を止め、既存の
`channels.json`を別名へ退避してから`setup`を実行します。既存ファイルを残したままでは、上書き防止のため新しい設定は
公開されません。LinuxとComposeの具体的な手順は[更新・切り戻し・削除](../operations/update-and-remove.md#チャンネル設定を更新する)にあります。

自動生成対象はKonomiTV v0.14.1と製品の2K範囲にあるサービスです。BS4Kなど対象外のサービスはチャンネル設定へ入りません。

## うまくいかない場合

| 状況 | 確認すること |
|---|---|
| `setup`のURLエラー | スキーム、ホスト、ポートを含むMirakurunまたはmirakcのURLか確認する |
| `setup`で空きチューナーがない | 録画とライブ視聴を止め、時間を置いて再実行する |
| `setup`が既存のチャンネル設定で失敗する | 既存`channels.json`を上書きせず残している。更新手順で別名へ退避する |
| `setup`が`BEHIND`で止まる | `db status`を確認し、必要な場合だけ`db migrate`を実行する |
| 番組表が空になる | `catalog sync`の成功、DBの`CURRENT`、Mirakurunへの到達性を確認する |
| 番組表が古い | `recording serve`が起動しているか、次の定期更新を待つ |
| チャンネルを選べない | `channels.json`と番組表のサービスが同じ接続先のものか確認する |

## 次に読むページ

- [KonomiTVと接続する](../getting-started/konomitv.md)
- [予約と録画を使う](recording.md)
- [ドキュメント一覧](../README.md)
