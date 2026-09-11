[README](../../README.md) / [ドキュメント](../README.md) / 更新・切り戻し・削除

# 更新・切り戻し・削除

Sazanami DVRの更新、切り戻し、アンインストールをまとめます。作業前に録画を停止し、バックアップを確認してください。

## 目次

- [Linuxの更新](#linuxの更新)
- [Composeの更新](#composeの更新)
- [切り戻し](#切り戻し)
- [アンインストール](#アンインストール)
- [録画ファイルを削除する](#録画ファイルを削除する)
- [困ったとき](#困ったとき)
- [次に読む](#次に読む)

## Linuxの更新

installerで管理しているLinux環境は、旧版を安全に取り外してから新版を配置します。`<old-version>`、`<new-version>`、`<arch>`は利用する版とCPUに合わせて置き換えてください。

新しい配布アーカイブを先に取得して展開しておきます。

```sh
tar -xzf /path/to/sazanami-dvr_<new-version>_linux_<arch>.tar.gz
```

録画が終了していることを確認し、次の順に実行します。

1. サービスを停止します。

   ```sh
   sudo systemctl stop sazanami-dvr.service
   ```

2. 旧版の実行ファイルでDBバックアップを作成します。表示された`backup_id`を控えてください。

   ```sh
   sudo -u sazanami-dvr /opt/sazanami-dvr/<old-version>/sazanami-dvr db backup --data-root /var/lib/sazanami-dvr
   ```

3. 旧版のinstallerでアンインストールします。設定、DB、録画、バックアップ、利用者アカウントは保持されます。

   ```sh
   sudo /opt/sazanami-dvr/<old-version>/packaging/install.sh uninstall
   ```

4. 新しいアーカイブのディレクトリへ移動し、installerでインストールします。

   ```sh
   cd sazanami-dvr_<new-version>_linux_<arch>
   sudo ./packaging/install.sh install
   ```

5. 新版のDB状態を確認します。

   ```sh
   sudo -u sazanami-dvr /opt/sazanami-dvr/<new-version>/sazanami-dvr db status --data-root /var/lib/sazanami-dvr
   ```

   `state=CURRENT`なら、DBの更新は不要です。`state=BEHIND`の場合だけ、次のコマンドを実行してから、もう一度状態を確認します。

   ```sh
   sudo -u sazanami-dvr /opt/sazanami-dvr/<new-version>/sazanami-dvr db migrate --data-root /var/lib/sazanami-dvr
   sudo -u sazanami-dvr /opt/sazanami-dvr/<new-version>/sazanami-dvr db status --data-root /var/lib/sazanami-dvr
   ```

6. DBが`CURRENT`になったら、番組表とチャンネル設定を確認します。

   ```sh
   sudo -u sazanami-dvr /opt/sazanami-dvr/<new-version>/sazanami-dvr catalog sync --data-root /var/lib/sazanami-dvr --provider mirakurun --base-url "<mirakurun-url>"
   sudo -u sazanami-dvr /opt/sazanami-dvr/<new-version>/sazanami-dvr ctrlcmd validate --data-root /var/lib/sazanami-dvr --channel-map /var/lib/sazanami-dvr/channels.json
   ```

7. サービスを有効化して起動します。

   ```sh
   sudo systemctl enable --now sazanami-dvr.service
   sudo systemctl status sazanami-dvr.service
   ```

`state=BEHIND`以外の状態やコマンドエラーが出た場合は、サービスを起動せず、表示された理由を確認してください。`SAZANAMI_DATA_ROOT`や`SAZANAMI_CHANNEL_MAP`を変更している場合は、各コマンドのパスも同じ値に置き換えます。

installer管理下の更新では、実行ファイルのシンボリックリンクを手動で切り替えません。

### 同じ版を再インストールする

同じ版を再インストールする場合も、先にサービスを停止します。利用中の配布アーカイブのディレクトリでinstallerを実行してください。

```sh
sudo systemctl stop sazanami-dvr.service
sudo ./packaging/install.sh install
```

## Composeの更新

Composeではホスト側のデータを残したままイメージを入れ替えます。作業ディレクトリは、Composeファイルを置いた場所です。

1. 録画が終了していることを確認し、コンテナを停止します。

   ```sh
   docker compose down
   ```

2. 現在のイメージでDBをバックアップし、表示された`backup_id`を控えます。

   ```sh
   docker compose run --rm --no-deps sazanami db backup \
     --data-root /var/lib/sazanami-dvr
   ```

3. `.env`の`SAZANAMI_IMAGE`を新しいイメージタグへ変更します。

   ```sh
   vi .env
   ```

4. 新しいイメージを取得し、版を確認します。

   ```sh
   docker compose pull sazanami
   docker compose run --rm sazanami --version
   ```

5. DBの状態を確認します。

   ```sh
   docker compose run --rm sazanami db status --data-root /var/lib/sazanami-dvr
   ```

   `state=CURRENT`なら、そのまま起動できます。`state=BEHIND`の場合だけ、次のコマンドを実行してから、状態を確認します。

   ```sh
   docker compose run --rm sazanami db migrate --data-root /var/lib/sazanami-dvr
   docker compose run --rm sazanami db status --data-root /var/lib/sazanami-dvr
   ```

6. DBが`CURRENT`になったら、サービスを起動します。

   ```sh
   docker compose up -d
   docker compose ps
   ```

Composeの更新では、`.env`の保存先やKonomiTVの設定、チャンネル設定を上書きする必要はありません。`state=BEHIND`以外の状態では起動せず、エラーを確認してください。Compose構成に自動purgeはありません。

## 切り戻し

### Linux

切り戻す版の配布アーカイブを先に用意します。installer管理下では、シンボリックリンクを手動で切り替えません。

DBの形式が変わっていない場合は、次の順で切り戻します。

```sh
sudo systemctl stop sazanami-dvr.service
sudo /opt/sazanami-dvr/<current-version>/packaging/install.sh uninstall
cd sazanami-dvr_<old-version>_linux_<arch>
sudo ./packaging/install.sh install
sudo -u sazanami-dvr /opt/sazanami-dvr/<old-version>/sazanami-dvr db status --data-root /var/lib/sazanami-dvr
```

`state=CURRENT`を確認してから、番組表とチャンネル設定を確認し、サービスを起動します。

```sh
sudo -u sazanami-dvr /opt/sazanami-dvr/<old-version>/sazanami-dvr catalog sync --data-root /var/lib/sazanami-dvr --provider mirakurun --base-url "<mirakurun-url>"
sudo -u sazanami-dvr /opt/sazanami-dvr/<old-version>/sazanami-dvr ctrlcmd validate --data-root /var/lib/sazanami-dvr --channel-map /var/lib/sazanami-dvr/channels.json
sudo systemctl enable --now sazanami-dvr.service
```

DBの形式が変わっている場合は、旧版へ戻す前に、サービスを停止した状態で、更新前に作成したバックアップを新版の実行ファイルで復元します。`phase=COMMITTED`と表示されたことを確認してから、旧版を配置します。

```sh
sudo systemctl stop sazanami-dvr.service
sudo -u sazanami-dvr /opt/sazanami-dvr/<current-version>/sazanami-dvr db restore --data-root /var/lib/sazanami-dvr --backup-id <backup-id>
# 出力が phase=COMMITTED であることを確認する
sudo /opt/sazanami-dvr/<current-version>/packaging/install.sh uninstall
cd sazanami-dvr_<old-version>_linux_<arch>
sudo ./packaging/install.sh install
sudo -u sazanami-dvr /opt/sazanami-dvr/<old-version>/sazanami-dvr db status --data-root /var/lib/sazanami-dvr
```

`state=CURRENT`を確認してから、番組表とチャンネル設定を確認し、サービスを起動します。

```sh
sudo -u sazanami-dvr /opt/sazanami-dvr/<old-version>/sazanami-dvr catalog sync --data-root /var/lib/sazanami-dvr --provider mirakurun --base-url "<mirakurun-url>"
sudo -u sazanami-dvr /opt/sazanami-dvr/<old-version>/sazanami-dvr ctrlcmd validate --data-root /var/lib/sazanami-dvr --channel-map /var/lib/sazanami-dvr/channels.json
sudo systemctl enable --now sazanami-dvr.service
```

復元が`COMMITTED`にならない場合や、旧版の`db status`が`CURRENT`にならない場合は起動せず、[バックアップと復元](backup-and-restore.md)を確認してください。起動前に番組表同期とチャンネル設定の検証も行います。

### Compose

DBの形式が変わっていない場合は、`.env`を旧イメージへ戻し、再作成します。

```sh
docker compose down
# .env の SAZANAMI_IMAGE を旧イメージへ戻す
docker compose pull sazanami
docker compose run --rm sazanami db status --data-root /var/lib/sazanami-dvr
```

`state=CURRENT`を確認してから起動します。

```sh
docker compose up -d
docker compose ps
```

DBの形式が変わっている場合は、停止後に新イメージでバックアップを復元し、`phase=COMMITTED`を確認してから、`.env`を旧イメージへ戻します。

```sh
docker compose down
docker compose run --rm sazanami db restore --data-root /var/lib/sazanami-dvr --backup-id <backup-id>
# 出力が phase=COMMITTED であることを確認する
# .env の SAZANAMI_IMAGE を旧イメージへ戻す
docker compose pull sazanami
docker compose run --rm sazanami db status --data-root /var/lib/sazanami-dvr
```

`state=CURRENT`を確認してから起動します。

```sh
docker compose up -d
docker compose ps
```

復元とDB状態の確認が終わるまで、Composeを起動しないでください。

## アンインストール

### Linuxの通常アンインストール

通常アンインストールでは、サービスを停止してから、現在のinstaller管理下のreleaseで実行します。

```sh
sudo systemctl stop sazanami-dvr.service
sudo /opt/sazanami-dvr/<version>/packaging/install.sh uninstall
```

次のものは保持されます。

- `/etc/sazanami-dvr`の設定
- `/var/lib/sazanami-dvr`のDB、録画、バックアップ
- 設定された外部録画先
- `sazanami-dvr`利用者とグループ

### Linuxのpurge

purgeは、installerが管理する標準のインストール先、設定、データ、利用者、グループを削除する不可逆操作です。サービスを停止し、対象を確認してから実行してください。

```sh
sudo systemctl stop sazanami-dvr.service
sudo /opt/sazanami-dvr/<version>/packaging/install.sh purge
```

確認プロンプトで`PURGE`と入力すると、次の標準対象が削除されます。

- `/opt/sazanami-dvr`
- `/etc/sazanami-dvr`
- `/var/lib/sazanami-dvr`
- `sazanami-dvr`利用者とグループ

設定で指定した標準外の録画先は削除されません。削除したデータは復元できないため、必要なファイルを先に退避してください。

### Composeの停止と削除

Composeで`down`やコンテナの削除を行っても、ホスト側の設定、DB、録画、バックアップ、ログ、キャプチャは削除されません。ホスト側のデータも削除する場合は、対象ディレクトリを確認してから、利用者自身で削除してください。Composeには自動purgeはありません。

```sh
docker compose down
```

`docker compose down -v`は、Compose管理のvolumeを削除するため、通常の停止では使用しません。

## 録画ファイルを削除する

標準Compose構成では、録画ディレクトリを読み取り専用でKonomiTVへ渡します。KonomiTVから録画を削除する場合は、削除用overlayを使います。

```sh
docker compose -f compose.yaml -f compose.konomitv-delete.yaml up -d
```

削除用overlayでは録画ディレクトリは書き込み可能になりますが、`.sazanami-dvr.lock`は読み取り専用のままです。削除後は録画一覧で`MISSING / FILE_MISSING`として扱われます。

削除作業が終わったら、通常構成へ戻します。

```sh
docker compose -f compose.yaml -f compose.konomitv-delete.yaml down
docker compose up -d
```

## 困ったとき

- DB状態が`CURRENT`にならない場合は、サービスを起動せず、`db status`の表示と直前のエラーを確認します。
- installerが停止を求める場合は、対象サービスが本当に停止していることを確認し、表示された理由を解消してから再実行します。
- バックアップや復元の`backup_id`は、手元の作業メモだけに保存します。公開Issueやログへ、認証情報・秘密情報・宅内環境の情報を貼らないでください。

## 次に読む

- [バックアップと復元](backup-and-restore.md)
- [ドキュメント一覧](../README.md)
