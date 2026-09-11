# Docker Composeで導入する

この構成では、Linux上でSazanami DVRとKonomiTV v0.14.1を一緒に起動します。Mirakurunまたはmirakc、チューナー、ドライバー、カードは含みません。先にホスト側でMirakurun互換APIを利用できる状態にしてください。番組情報の準備は[Mirakurunから番組情報を取得する](mirakurun-catalog-sync.md)、チャンネル設定は[CtrlCmdチャンネル待受の使い方](ctrlcmd-channel-runtime.md)を参照してください。

初回は「最短セットアップ」を上から順に実行します。更新、録画削除、切り戻しでは、後半の該当する章だけを参照できます。

## 最短セットアップ

配布アーカイブの`packaging/compose`を、設定と録画を保存するディレクトリへコピーします。以降のコマンドはコピー先で実行してください。

```sh
cp -R packaging/compose <install-dir>
cd <install-dir>
./prepare.sh
```

`prepare.sh`が設定ファイルと保存先を作ります。次の3か所だけを準備します。

1. `.env`の`MIRAKURUN_URL`を編集する。
2. `config/konomitv.yaml`の`mirakurun_url`を同じURLへ変更する。
3. 用意したチャンネル設定を`data/sazanami/channels.json`へ置く。

設定が済んだら、次のブロックをそのまま上から実行します。

```sh
docker compose build konomitv
docker compose run --rm sazanami db migrate \
  --data-root /var/lib/sazanami-dvr
docker compose run --rm sazanami db status \
  --data-root /var/lib/sazanami-dvr
docker compose run --rm sazanami catalog sync \
  --data-root /var/lib/sazanami-dvr \
  --provider mirakurun \
  --base-url "$(sed -n 's/^MIRAKURUN_URL=//p' .env)"
docker compose run --rm sazanami ctrlcmd validate \
  --data-root /var/lib/sazanami-dvr \
  --channel-map /var/lib/sazanami-dvr/channels.json
docker compose up -d
docker compose ps
```

`db status`が`state=CURRENT`になり、最後の`docker compose ps`で両サービスが起動していれば準備完了です。KonomiTVはSazanami DVRのヘルスチェック成功後に起動します。Web画面はホストのポート7200で開けます。

KonomiTVの初回ビルドでは、公式リポジトリの固定commitから依存パッケージを取得します。処理に時間がかかることがあります。途中のコマンドが失敗した場合は`docker compose up -d`へ進まず、表示されたエラーを確認してください。

## この構成で使うファイルと接続先

標準構成は[`compose.yaml`](../packaging/compose/compose.yaml)です。[`.env.example`](../packaging/compose/.env.example)と[`konomitv.yaml.example`](../packaging/compose/konomitv.yaml.example)から、`prepare.sh`が編集用の設定を作ります。

- Sazanami DVRはGHCRの完全なバージョンタグを使います。
- KonomiTVはタグ`v0.14.1`、コミット`0a32188274b81c1e7bed642474b208bd2a543a6b`の公式Dockerfileからローカルイメージを作ります。
- 両サービスは、`prepare.sh`を実行した利用者と同じUID/GIDで動きます。
- Sazanami DVRのCtrlCmdと録画HTTPは`127.0.0.1:4520`と`127.0.0.1:4521`で待ち受けます。
- KonomiTVにはホスト全体を渡さず、録画ディレクトリだけを読み取り専用で渡します。

`prepare.sh`は既存の設定を上書きしません。録画ディレクトリの管理用ファイルがシンボリックリンク、特殊ファイル、別の所有者、不正な権限、複数のハードリンクのいずれかに当たる場合も、変更せずに終了します。

## KonomiTVから録画を削除する

既定では、KonomiTVに録画ディレクトリを読み取り専用で渡します。KonomiTVの管理者画面から録画を削除する場合だけ、基底の`compose.yaml`と削除用の`compose.konomitv-delete.yaml`を指定して起動します。

```sh
docker compose \
  -f compose.yaml \
  -f compose.konomitv-delete.yaml \
  up -d
```

削除用のCompose構成では、Sazanami DVRとKonomiTVを同じホストのUID/GIDで動かします。KonomiTVは録画ディレクトリの信頼済み共同所有者として扱われ、録画ファイルを削除・置換できます。Sazanami DVRが所有するロックファイルだけは、同じパスへ個別の読み取り専用マウントとして渡します。ホストのルートファイルシステム、Sazanami DVRのデータ領域、SQLite、Dockerソケットは渡しません。

Sazanami DVRは完了録画を1分ごとに最大1,000件ずつ照合します。KonomiTVが完成ファイルを削除すると、録画履歴を残したまま状態が`MISSING / FILE_MISSING`へ変わります。所有者、権限、ファイル種別、リンク数、期待サイズが正しい完成ファイルを同じパスへ戻すと、次の照合で状態が`FINAL`へ戻ります。ファイルの内容は比較しないため、同じサイズの別内容でも`FINAL`になります。

Sazanami DVRは、KonomiTVのDB、サムネイル、補助ファイル、録画ディレクトリ内の未知ファイルを変更しません。ファイルを確認した直後に外部変更が重なると、一時的に古い状態を表示しますが、次の照合で更新します。

削除機能を無効に戻す場合は、削除用のCompose構成で起動したコンテナを止め、基底の`compose.yaml`だけで作り直します。録画ファイルと2つのDBは削除しません。

```sh
docker compose \
  -f compose.yaml \
  -f compose.konomitv-delete.yaml \
  down
docker compose -f compose.yaml up -d
```

## 更新する

録画中でないことと、直近の予約に停止時間が重ならないことを確認します。まずSazanami DVRのバックアップIDを記録します。

```sh
docker compose run --rm sazanami db backup \
  --data-root /var/lib/sazanami-dvr
docker compose down
```

`.env`の`SAZANAMI_IMAGE`を新しい完全なバージョンタグへ変更し、イメージを取得します。

```sh
docker compose pull sazanami
docker compose run --rm sazanami --version
docker compose run --rm sazanami db status \
  --data-root /var/lib/sazanami-dvr
```

`BEHIND`の場合だけ、新版で`db migrate`を実行します。`CURRENT`を確認してから`docker compose up -d`を
実行してください。`.env`、KonomiTV設定、チャンネル設定を自動上書きしません。

## 切り戻す

DBスキーマが変わっていなければ、停止後に`SAZANAMI_IMAGE`を旧版の完全なバージョンタグへ戻し、旧版の`db status`が
`CURRENT`であることを確認して起動します。

DBスキーマが変わった場合は、新版イメージのまま更新前のバックアップを復元します。

```sh
docker compose down
docker compose run --rm sazanami db restore \
  --data-root /var/lib/sazanami-dvr \
  --backup-id <更新前のbackup-id>
```

出力に`phase=COMMITTED`と表示されたことを確認し、旧版のバージョンタグへ戻します。旧版の`db status`が`CURRENT`に
なってから起動してください。録画ファイルは削除、移動、上書きしません。

## コンテナを停止・削除する（データは残す）

```sh
docker compose stop
docker compose down
```

これらはコンテナを止めますが、ホストからマウントしたデータ、設定、録画、ログ、キャプチャを削除しません。通常手順では`down -v`やホスト側ディレクトリの削除を実行しません。このCompose構成に自動purgeはありません。すべて消す場合は`down`の完了後、専用に作成したコピー先と各保存先の絶対パスを1件ずつ確認してから手動で削除します。

## 制限

- KonomiTVの録画マウントは既定で読み取り専用です。削除用のCompose構成だけ、KonomiTV画面から削除できます。
- 削除用のCompose構成では、KonomiTVがロックファイル以外の録画ディレクトリ内を変更できます。同じサイズの内容変更は検出しません。
- Compose全体のarm64、GPUエンコーダー、Docker Desktop、rootless Dockerは未検証です。
- KonomiTV／Komorebiの全操作対応を示す構成ではありません。
- ソースからのビルドだけでは、番組表、途中録画、停止、再生の実通信成功を証明しません。

手順の途中で`db status`が`CURRENT`にならない場合は起動せず、表示された理由と製品バージョンを[GitHub Issues](https://github.com/g0ooo0gle/sazanami-dvr/issues)へ報告してください。脆弱性に関する連絡は[SECURITY.md](../SECURITY.md)を参照してください。
