# Linuxへ導入し、安全に更新・削除する

この手順は、systemdを使うLinuxへGitHub Releaseの配布アーカイブを導入する人向けです。初回は「アーカイブを展開して1コマンドで配置する」から「サービスを起動する」まで順に進めます。更新、切り戻し、削除では必要な章だけを参照してください。対応範囲と未検証項目は[互換実装表](compatibility.md)にまとめています。

ここでは、`<version>`を導入する版、`<arch>`を`amd64`または`arm64`へ読み替えます。先にMirakurunまたはmirakcと`channels.json`を用意してください。詳しい作り方は[Mirakurunから番組情報を取得する](mirakurun-catalog-sync.md)と[CtrlCmdチャンネル待受の使い方](ctrlcmd-channel-runtime.md)にあります。

## アーカイブを展開して1コマンドで配置する

GitHub ReleaseからCPUに合うアーカイブを取得し、展開したディレクトリでインストーラを実行します。

```console
tar -xzf sazanami-dvr_<version>_linux_<arch>.tar.gz
cd sazanami-dvr_<version>_linux_<arch>
sudo ./packaging/install.sh install
```

インストーラは専用利用者、標準ディレクトリ、実行ファイル、systemdのサービス定義、初回の環境設定を配置します。DBの更新、番組表の取得、チャンネル設定、サービスの起動は行いません。途中で安全条件に合わない状態を見つけた場合は、既存のファイルを置き換えずに終了します。

同じ版をもう一度実行しても設定とデータは変わりません。別の版へ更新する場合は、後半の「新しい版へ更新する」へ進んでください。

## 接続先とチャンネルを設定する

まず環境設定を開き、`SAZANAMI_MIRAKURUN_URL`を実際のMirakurunまたはmirakcへ変更します。録画先や待受先を変えない場合、ほかの項目は初期値のまま使えます。

```console
sudoedit /etc/sazanami-dvr/sazanami-dvr.env
```

チャンネル設定は、準備したJSONを次の場所へ置きます。

```console
sudo install -o root -g sazanami-dvr -m 0640 \
  ./channels.json \
  /var/lib/sazanami-dvr/channels.json
```

既定の待受先は次のとおりです。CtrlCmdは`0.0.0.0:4520`、録画履歴HTTPは`127.0.0.1:4521`、手動起動するWebUIは`127.0.0.1:4522`で待ち受けます。CtrlCmdには認証とTLSがないため、信頼できるLANの外へ公開しないでください。同じPCからだけ接続する場合は、CtrlCmdの待受先を`127.0.0.1:4520`へ変更します。

## DBと番組表を準備する

次のブロックを上から順に実行します。`<mirakurun-url>`には環境設定と同じURLを指定してください。

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db migrate \
  --data-root /var/lib/sazanami-dvr

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db status \
  --data-root /var/lib/sazanami-dvr
```

最後の表示が`state=CURRENT`でなければ、サービスを起動しないでください。続けて番組表を一度取得し、チャンネル設定を確認します。

```console
sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr catalog sync \
  --data-root /var/lib/sazanami-dvr \
  --provider mirakurun \
  --base-url <mirakurun-url>

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr ctrlcmd validate \
  --data-root /var/lib/sazanami-dvr \
  --channel-map /var/lib/sazanami-dvr/channels.json
```

## サービスを起動する

インストーラが配置したsystemdユニットを有効化して起動します。

```console
sudo systemctl enable --now sazanami-dvr.service
sudo systemctl status sazanami-dvr.service
```

このユニットは、失敗時だけ5秒後に再起動します。停止時はSIGTERMを送り、録画処理の終了を最長2分待ちます。DBの自動更新と番組表の初回作成は行いません。

`Active: active (running)`と表示されれば起動完了です。環境設定を変えた場合は`sudo systemctl restart sazanami-dvr.service`で反映してください。WebUIを使う場合は、環境設定例にある`127.0.0.1:4522`を`ui serve --listen`の引数に指定して、別途手動で起動します。

## 配置先と残るデータ

実行ファイルと設定・データは分かれています。通常のアンインストールでは、設定とデータを残します。

| 用途 | パス | 通常削除時 |
|---|---|---|
| 版ごとの配布物 | `/opt/sazanami-dvr/<version>/` | 削除する |
| 実行ファイルのリンク | `/usr/local/bin/sazanami-dvr` | 削除する |
| 環境設定 | `/etc/sazanami-dvr/` | 残す |
| チャンネル設定、DB、バックアップ、既定録画先 | `/var/lib/sazanami-dvr/` | 残す |

設定、DB、バックアップ、既定録画先まで消す操作は、末尾のpurgeだけです。

### 同時録画数は通常、自動で決まる

標準のsystemdユニットは`--max-concurrent-recordings`を渡しません。サービスを起動するたびに`GET /api/tuners`を1回だけ実行し、Mirakurunの設定台数を同時録画数に使います。取得に失敗した場合は1件で起動します。20件以上でも制限せず、負荷が増える可能性を1回だけ表示します。

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

ほかの引数を変える場合も、同じように`ExecStart`全体を明示します。利用できる引数は`/usr/local/bin/sazanami-dvr recording serve --help`で確認してください。

## インストーラを使わずに配置する

既存の利用者や独自の配置を引き継ぐ必要がある場合は、手動で導入できます。次は標準配置の最小例です。

```console
sudo useradd --system --user-group --home-dir /var/lib/sazanami-dvr \
  --shell /usr/sbin/nologin sazanami-dvr
sudo install -d -o root -g root -m 0755 /opt/sazanami-dvr/<version>
sudo install -d -o root -g sazanami-dvr -m 0750 /etc/sazanami-dvr
sudo install -d -o sazanami-dvr -g sazanami-dvr -m 0700 \
  /var/lib/sazanami-dvr /var/lib/sazanami-dvr/recordings

sudo tar -xzf sazanami-dvr_<version>_linux_<arch>.tar.gz \
  -C /opt/sazanami-dvr/<version> --strip-components=1
sudo ln -s /opt/sazanami-dvr/<version>/sazanami-dvr \
  /usr/local/bin/sazanami-dvr
sudo ln -s /opt/sazanami-dvr/<version>/packaging/systemd/sazanami-dvr.service \
  /etc/systemd/system/sazanami-dvr.service
sudo install -o root -g root -m 0600 \
  /opt/sazanami-dvr/<version>/packaging/systemd/sazanami-dvr.env.example \
  /etc/sazanami-dvr/sazanami-dvr.env
sudo systemctl daemon-reload
```

この方法で配置した環境は、同梱インストーラの管理対象になりません。この文書のインストーラ向け更新、削除、purgeも使用できないため、配置先を記録して独自に管理してください。配置後は「接続先とチャンネルを設定する」に戻って初期設定を続けます。

## 新しい版へ更新する

録画中でないこと、また直近の予約が停止時間と重ならないことを先に確認します。更新中はSazanami DVRを停止します。

```console
sudo systemctl stop sazanami-dvr.service

sudo -u sazanami-dvr /usr/local/bin/sazanami-dvr db backup \
  --data-root /var/lib/sazanami-dvr
```

成功時に表示された`backup_id`を、更新が完了するまで手元に控えておきます。

v0.1.1の標準設定から更新する場合、環境設定は自動で書き換わりません。`SAZANAMI_CHANNEL_MAP`が旧配置を指している場合だけ、サービスを停止したまま次を実行します。すでにデータ保存先の直下を指している場合は、この手順を飛ばしてください。

```console
sudo install -o root -g sazanami-dvr -m 0640 \
  /etc/sazanami-dvr/channels.json \
  /var/lib/sazanami-dvr/channels.json

sudoedit /etc/sazanami-dvr/sazanami-dvr.env
```

環境設定の該当行を次の値へ変更します。新版の起動を確認するまで、コピー元のファイルは削除しないでください。

```ini
SAZANAMI_CHANNEL_MAP=/var/lib/sazanami-dvr/channels.json
```

次に、新版を旧版とは別のディレクトリへ展開します。

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

新版の`db status`が`CURRENT`になったら、実行ファイルとサービス定義の2つのリンクを切り替えます。設定ファイルは上書きしません。

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

出力に`phase=COMMITTED`と表示されたことを確認してから、旧版で`db status`を実行します。復元が中断した場合はサービスを起動せず、[中断した復元を再開する手順](catalog-database-operations.md#中断した復元を再開する)へ進んでください。

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

インストーラで配置した環境は、次の2コマンドで削除できます。サービスと配布物だけを外し、環境設定、チャンネル設定、DB、録画、バックアップ、専用利用者は残します。

```console
sudo systemctl stop sazanami-dvr.service
sudo /opt/sazanami-dvr/<version>/packaging/install.sh uninstall
```

再導入時は、配布アーカイブから`sudo ./packaging/install.sh install`を実行すると既存の設定とデータを引き継げます。新しい実行ファイルで`db status`を確認してから起動してください。

手動で配置した環境には管理マーカーがないため、インストーラでは削除できません。その場合はサービスを停止して無効化し、実行ファイルへのリンク、systemdサービス定義へのリンク、`/opt/sazanami-dvr`の順に、実際の配置先を1つずつ確認してから削除します。

## すべてのデータを明示的にpurgeする

この操作は元に戻せません。録画が不要で、必要なバックアップを別の場所へ退避した場合だけ実行してください。

インストーラで配置した環境では、サービスを停止して`purge`を直接実行します。削除対象が表示されたら確認し、続行する場合だけ`PURGE`と入力してください。

```console
sudo systemctl stop sazanami-dvr.service
sudo /opt/sazanami-dvr/<version>/packaging/install.sh purge
```

`purge`は`/opt/sazanami-dvr`、`/etc/sazanami-dvr`、`/var/lib/sazanami-dvr`、専用利用者とグループだけを削除します。標準のデータディレクトリ外に設定した録画先は自動削除せず、録画も残ります。外部ディスクや別の録画先を削除するときは、マウント状態、絶対パス、内容を1つずつ確認してから手動で行ってください。

管理マーカーの不一致、シンボリックリンク、特殊ファイル、所有者の相違、マウントポイントを検出すると、インストーラは自動で`purge`を行いません。手動配置した環境も同様です。表示された理由を確認し、削除対象を特定できない場合は作業を止めてください。

## 用語

- **通常削除**: サービス、実行リンク、版別配布物だけを取り除き、設定とデータを残す操作。
- **purge**: 利用者が明示的に選び、設定、DB、バックアップ、録画、専用利用者まで削除する操作。
- **切り戻し**: 以前の実行ファイルへ戻す操作。DB形式が変わった場合は、更新前バックアップの復元も含む。

手順どおりに進めても`db status`が`CURRENT`にならない場合は、通常起動や削除を続けないでください。[GitHub Issues](https://github.com/g0ooo0gle/sazanami-dvr/issues)には、表示された理由と製品バージョンだけを報告します。接続先、番組名、絶対パス、DBや設定ファイルそのものは添付しないでください。脆弱性に関する連絡は[SECURITY.md](../SECURITY.md)を参照してください。
