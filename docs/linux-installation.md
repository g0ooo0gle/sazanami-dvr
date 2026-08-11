# Linuxへ導入し、安全に更新・削除する

この手順は、systemdを使うLinuxへGitHub Releaseの配布アーカイブを導入する人向けです。初めて導入する場合は「初回導入」から「サービスを起動する」まで順に進めてください。更新、切り戻し、削除では、必要な章だけを参照できます。

ここでは、`<version>`を導入する版、`<arch>`を`amd64`または`arm64`へ読み替えます。Mirakurunとチャンネル設定の準備は、[Mirakurunから番組情報を取得する](mirakurun-catalog-sync.md)と[CtrlCmdチャンネル待受の使い方](ctrlcmd-channel-runtime.md)も参照してください。

## 配置先を先に確認する

標準手順では、版ごとの実行ファイルを`/opt`へ残し、`/usr/local/bin/sazanami-dvr`が利用中の版を指します。設定とデータは実行ファイルから分けます。

| 用途 | パス | 通常削除時 |
|---|---|---|
| 版ごとの配布物 | `/opt/sazanami-dvr/<version>/` | 削除する |
| 実行ファイルのリンク | `/usr/local/bin/sazanami-dvr` | 削除する |
| 設定 | `/etc/sazanami-dvr/` | 残す |
| DB、バックアップ、既定録画先 | `/var/lib/sazanami-dvr/` | 残す |

設定、DB、録画、バックアップを消すのは、末尾の「すべてのデータを明示的にpurgeする」だけです。

## 初回導入

### 1. 配布物のハッシュを確認する

GitHub Releaseから、利用するCPU向けのアーカイブと`SHA256SUMS`を同じリリースから取得します。ダウンロードしたアーカイブだけを照合できます。

```console
sha256sum --ignore-missing --check SHA256SUMS
```

対象アーカイブが`OK`にならない場合は、そのファイルを展開しないでください。

### 2. 専用利用者とディレクトリを作る

次の操作にはroot権限が必要です。`sazanami-dvr`利用者は対話ログインに使いません。

```console
sudo useradd --system \
  --user-group \
  --home-dir /var/lib/sazanami-dvr \
  --shell /usr/sbin/nologin \
  sazanami-dvr

sudo install -d -o root -g root -m 0755 /opt/sazanami-dvr/<version>
sudo install -d -o root -g sazanami-dvr -m 0750 /etc/sazanami-dvr
sudo install -d -o sazanami-dvr -g sazanami-dvr -m 0700 \
  /var/lib/sazanami-dvr \
  /var/lib/sazanami-dvr/recordings
```

### 3. 新しい版を展開する

```console
sudo tar -xzf sazanami-dvr_<version>_linux_<arch>.tar.gz \
  -C /opt/sazanami-dvr/<version> \
  --strip-components=1

sudo ln -s /opt/sazanami-dvr/<version>/sazanami-dvr \
  /usr/local/bin/sazanami-dvr

/usr/local/bin/sazanami-dvr --version
```

表示された版がアーカイブの版と異なる場合は、DB操作へ進まないでください。

### 4. 設定を初回だけ作る

環境設定例をコピーし、MirakurunまたはmirakcのURLを編集します。更新時は、このファイルを上書きしません。

```console
sudo install -o root -g root -m 0600 \
  /opt/sazanami-dvr/<version>/packaging/systemd/sazanami-dvr.env.example \
  /etc/sazanami-dvr/sazanami-dvr.env

sudoedit /etc/sazanami-dvr/sazanami-dvr.env
```

設定例には、データと録画の保存先、チャンネル設定、MirakurunのURLに加え、次の待受先があります。

- CtrlCmd: `0.0.0.0:4520`
- 録画履歴HTTP: `127.0.0.1:4521`
- WebUI: `127.0.0.1:4522`

CtrlCmdは信頼できるLANから接続するための設定です。同じPCからだけ接続する場合は`127.0.0.1:4520`へ変更してください。WebUIはこのサービスから起動しませんが、手動起動時に同じ設定値を確認できるよう、環境設定例へ記載しています。

チャンネル設定は、準備したJSONを所有者だけが変更できる形で配置します。

```console
sudo install -o root -g sazanami-dvr -m 0640 \
  ./channels.json \
  /etc/sazanami-dvr/channels.json
```

### 5. DBと番組表を明示的に準備する

Sazanami DVRはサービス起動時にDBを自動更新しません。初回だけ、専用利用者で次を実行します。

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db migrate \
  --data-root /var/lib/sazanami-dvr

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr
```

最後の表示が`state=CURRENT`でなければ、サービスを起動しません。続けて番組表を一度取得し、チャンネル設定を確認します。`<mirakurun-url>`は環境設定ファイルと同じURLへ置き換えてください。

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr catalog sync \
  --data-root /var/lib/sazanami-dvr \
  --provider mirakurun \
  --base-url <mirakurun-url>

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr ctrlcmd validate \
  --data-root /var/lib/sazanami-dvr \
  --channel-map /etc/sazanami-dvr/channels.json
```

## サービスを起動する

配布物のunitへリンクを作ります。版を切り替えるときは、実行ファイルとunitを同じ版へそろえます。

```console
sudo ln -s /opt/sazanami-dvr/<version>/packaging/systemd/sazanami-dvr.service \
  /etc/systemd/system/sazanami-dvr.service

sudo systemctl daemon-reload
sudo systemctl enable --now sazanami-dvr.service
sudo systemctl status sazanami-dvr.service
```

unitは失敗時だけ5秒後に再起動します。停止時はSIGTERMを送り、録画処理の終了を最長2分待ちます。DBの自動更新と番組表の初回作成は行いません。

CtrlCmdと録画履歴HTTPの待受先は、環境設定ファイルから明示的に読みます。変更した場合は`sudo systemctl restart sazanami-dvr.service`で反映してください。WebUIを使う場合は、環境設定例にある`127.0.0.1:4522`を`ui serve --listen`へ指定して手動で起動します。

### 同時録画数は通常、自動で決まる

基準unitは`--max-concurrent-recordings`を渡しません。サービスを起動するたびに`GET /api/tuners`を一度だけ実行し、Mirakurunの設定台数を同時録画数に使います。取得に失敗した場合は一件で起動します。20件以上でも制限せず、負荷が増える可能性を一度だけ表示します。

同時録画数を固定する場合は、正の整数をsystemd drop-inへ明示します。`sudo systemctl edit sazanami-dvr.service`を実行し、次の内容を保存してください。この指定がある場合、起動時のチューナー一覧取得は行いません。

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/sazanami-dvr recording serve --data-root ${SAZANAMI_DATA_ROOT} --recording-root ${SAZANAMI_RECORDING_ROOT} --channel-map ${SAZANAMI_CHANNEL_MAP} --provider mirakurun --base-url ${SAZANAMI_MIRAKURUN_URL} --listen ${SAZANAMI_CTRLCMD_LISTEN} --http-listen ${SAZANAMI_RECORDING_HTTP_LISTEN} --max-concurrent-recordings 2
```

値を変えた後はサービスを再起動します。

```console
sudo systemctl daemon-reload
sudo systemctl restart sazanami-dvr.service
```

ほかの引数を変える場合も、同じように`ExecStart`全体を明示します。利用できる引数は[README](../README.md)で確認してください。

## 新しい版へ更新する

録画中でないことと、直近の予約に停止時間が重ならないことを先に確認します。更新中はSazanami DVRを停止します。

```console
sudo systemctl stop sazanami-dvr.service

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db backup \
  --data-root /var/lib/sazanami-dvr
```

成功時に表示された`backup_id`を、更新が完了するまで手元へ控えます。次に、新版を旧版とは別のディレクトリへ展開します。

```console
sudo install -d -o root -g root -m 0755 /opt/sazanami-dvr/<new-version>
sudo tar -xzf sazanami-dvr_<new-version>_linux_<arch>.tar.gz \
  -C /opt/sazanami-dvr/<new-version> \
  --strip-components=1

/opt/sazanami-dvr/<new-version>/sazanami-dvr --version
```

新版の実行ファイルでDB状態を確認します。

```console
sudo -u sazanami-dvr \
  /opt/sazanami-dvr/<new-version>/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr
```

`state=BEHIND`の場合だけ、停止したまま新版の`db migrate`を実行します。`CURRENT`ならDB更新は不要です。それ以外の状態では、リンクを切り替えず調査してください。

```console
sudo -u sazanami-dvr \
  /opt/sazanami-dvr/<new-version>/sazanami-dvr db migrate \
  --data-root /var/lib/sazanami-dvr
```

新版の`db status`が`CURRENT`になったら、二つのリンクを切り替えます。設定ファイルは上書きしません。

```console
sudo ln -sfn /opt/sazanami-dvr/<new-version>/sazanami-dvr \
  /usr/local/bin/sazanami-dvr
sudo ln -sfn /opt/sazanami-dvr/<new-version>/packaging/systemd/sazanami-dvr.service \
  /etc/systemd/system/sazanami-dvr.service

sudo systemctl daemon-reload
sudo systemctl start sazanami-dvr.service
sudo systemctl status sazanami-dvr.service
```

旧版ディレクトリと更新前バックアップは、番組表、予約、録画の通常運転を確認するまで残します。

## 以前の版へ切り戻す

まずサービスを停止します。新旧でDB形式が同じなら、リンクを旧版へ戻し、旧版の`db status`が`CURRENT`であることを確認してから起動します。

DB形式が更新されている場合は、新版の実行ファイルを使って更新前バックアップを復元します。`<backup-id>`には、更新前に控えた値を指定します。

```console
sudo systemctl stop sazanami-dvr.service

sudo -u sazanami-dvr \
  /opt/sazanami-dvr/<new-version>/sazanami-dvr db restore \
  --data-root /var/lib/sazanami-dvr \
  --backup-id <backup-id>
```

`phase=COMMITTED`を確認してから、旧版の状態を読みます。復元が中断した場合はサービスを起動せず、[中断した復元を再開する手順](catalog-database-operations.md#中断した復元を再開する)へ進んでください。

```console
sudo -u sazanami-dvr \
  /opt/sazanami-dvr/<old-version>/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr

sudo ln -sfn /opt/sazanami-dvr/<old-version>/sazanami-dvr \
  /usr/local/bin/sazanami-dvr
sudo ln -sfn /opt/sazanami-dvr/<old-version>/packaging/systemd/sazanami-dvr.service \
  /etc/systemd/system/sazanami-dvr.service

sudo systemctl daemon-reload
sudo systemctl start sazanami-dvr.service
sudo systemctl status sazanami-dvr.service
```

切り戻しでは設定と録画ファイルを削除、移動、上書きしません。

## 通常のアンインストールではデータを残す

通常削除は、サービスと配布物だけを外します。設定、DB、録画、バックアップ、専用利用者は残ります。

```console
sudo systemctl disable --now sazanami-dvr.service
sudo rm -f -- /etc/systemd/system/sazanami-dvr.service
sudo systemctl daemon-reload

sudo rm -f -- /usr/local/bin/sazanami-dvr
sudo rm -rf -- /opt/sazanami-dvr
```

再導入する場合は、既存の`sazanami-dvr`利用者、`/etc/sazanami-dvr/`、`/var/lib/sazanami-dvr/`をそのまま使えます。新しい実行ファイルで`db status`を確認してから起動してください。

## すべてのデータを明示的にpurgeする

この章は元に戻せない削除です。録画が不要で、必要なバックアップを別の場所へ退避したことを確認した場合だけ実行します。録画保存先を`/var/lib/sazanami-dvr/recordings`以外へ変えた場合は、その保存先を別に確認してください。

先に通常のアンインストールを完了します。その後、固定した二つの製品ディレクトリだけを削除し、最後に専用利用者を削除します。各行が成功したことを確認してから次へ進んでください。

```console
sudo rm -rf -- /etc/sazanami-dvr
sudo rm -rf -- /var/lib/sazanami-dvr
sudo userdel sazanami-dvr
sudo groupdel sazanami-dvr
```

`userdel`が専用グループも削除した環境では、最後の`groupdel`は「グループが存在しない」と表示して終了します。これはデータ削除の失敗ではありません。別の録画保存先は、絶対パスと内容を一つずつ確認してから手動で削除してください。ワイルドカード、未確認の環境変数、`/var`などの広い親ディレクトリは削除対象にしません。

## 用語

- **通常削除**: サービス、実行リンク、版別配布物だけを取り除き、設定とデータを残す操作。
- **purge**: 利用者が明示的に選び、設定、DB、バックアップ、録画、専用利用者まで削除する操作。
- **切り戻し**: 以前の実行ファイルへ戻す操作。DB形式が変わった場合は、更新前バックアップの復元も含む。

手順どおりに進めても`db status`が`CURRENT`にならない場合は、通常起動や削除を続けず、GitHubのIssueへ表示された固定理由と製品バージョンを報告してください。接続先、番組名、絶対パス、DBや設定ファイルそのものは添付しないでください。
