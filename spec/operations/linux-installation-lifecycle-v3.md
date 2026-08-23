# Linux導入・更新・削除仕様 v3

- Status: Accepted
- Date: 2026-08-23
- Applies to: Sazanami DVR v0.5.0以後
- Decisions: ADR-0051、ADR-0058、ADR-0069
- Supersedes: `spec/operations/linux-installation-lifecycle-v2.md`

## 目的

Linux配布アーカイブから、初回導入、更新、旧版への切り戻し、通常削除を再現できるようにする。
設定、DB、録画、バックアップは、利用者がpurgeを明示しない限り削除しない。

V3は、版別配置、systemd、明示DB更新、切り戻し、通常削除、purgeを維持する。
`SHA256SUMS`は生成・公開せず、利用者向けの取得・照合手順や専用機能も追加しない。

## 配布アーカイブ

Linux amd64版とarm64版の各アーカイブには、次を含める。

- `sazanami-dvr`実行ファイル
- `LICENSE`、`THIRD_PARTY_NOTICES.md`、`README.md`、`CHANGELOG.md`
- `docs/`以下の公開手順と互換実装表
- `packaging/systemd/sazanami-dvr.service`
- `packaging/systemd/sazanami-dvr.env.example`

`SHA256SUMS`は必須成果物にしない。初回導入でチェックサムの取得や照合を要求せず、そのための製品機能や
専用スクリプトも追加しない。Unitと設定例に秘密、実環境の接続先、利用者名、私的な絶対パスを含めない。

## 配置と権限

| 対象 | 既定path | 所有者・mode |
|---|---|---|
| 配布物 | `/opt/sazanami-dvr/<version>/` | `root:root`、ディレクトリ0755、実行ファイル0755 |
| 実行リンク | `/usr/local/bin/sazanami-dvr` | `root:root` |
| 設定ディレクトリ | `/etc/sazanami-dvr/` | `root:sazanami-dvr`、0750 |
| 環境設定 | `/etc/sazanami-dvr/sazanami-dvr.env` | `root:root`、0600 |
| Data root | `/var/lib/sazanami-dvr/` | `sazanami-dvr:sazanami-dvr`、0700 |
| チャンネル設定 | `/var/lib/sazanami-dvr/channels.json` | `root:sazanami-dvr`、0640 |
| 既定録画先 | `/var/lib/sazanami-dvr/recordings/` | `sazanami-dvr:sazanami-dvr`、0700 |

チャンネル設定はdata root直下の通常ファイルとする。Data root外、symlink、ディレクトリは使わない。この条件は
`ctrlcmd validate`と`recording serve`の既存パス検査に合わせる。

環境設定例はdata root、録画先、チャンネル設定、Mirakurun URLと、次の三項目を持つ。

- CtrlCmd: `SAZANAMI_CTRLCMD_LISTEN=0.0.0.0:4520`
- 録画HTTP: `SAZANAMI_RECORDING_HTTP_LISTEN=127.0.0.1:4521`
- WebUI: `SAZANAMI_WEBUI_LISTEN=127.0.0.1:4522`

systemd unitはCtrlCmdと録画HTTPの設定を`recording serve`へ渡す。WebUIの設定は手動で
`ui serve --listen`を実行するときに使い、このunitからWebUIを起動しない。更新間隔は実行ファイルの
既定値を使う。

基準unitと環境設定例には、`--max-concurrent-recordings`と同時録画数用の環境変数を置かない。Flag未指定の
まま起動し、ADR-0050で定めたMirakurunチューナー数の一回取得を使う。同時録画数を固定する場合は、
systemd drop-inで`ExecStart`を一度消してから、基準unitと同じ引数に
`--max-concurrent-recordings <正の整数>`を加えた一行へ置き換える。空値や複数引数を展開する環境変数は使わない。

## systemd unit

Unitは次を満たす。

- `User=sazanami-dvr`と`Group=sazanami-dvr`を使う。
- `/etc/sazanami-dvr/sazanami-dvr.env`を必須の`EnvironmentFile`として読む。
- `recording serve`を一つだけ起動し、CtrlCmdと録画HTTPの待受を環境設定から明示する。
- 基準`ExecStart`では`--max-concurrent-recordings`を渡さない。
- `Restart=on-failure`、`RestartSec=5s`とする。
- `KillSignal=SIGTERM`、`TimeoutStopSec=2min`とする。
- `UMask=0077`とする。
- DB、catalog、設定を`ExecStartPre`または`ExecStartPost`で変更しない。
- Shell、`sudo`、任意の子コマンドを使わない。

## 初回導入

1. GitHub Releaseから対象CPUのアーカイブを取得する。
2. 専用利用者と配置先を作る。
3. `/opt/sazanami-dvr/<version>/`へ展開し、実行リンクを作る。
4. 環境設定例を初回だけコピーし、Mirakurun URLを設定する。
5. チャンネル設定を`/var/lib/sazanami-dvr/channels.json`へ配置する。
6. `db status`、`db migrate`、`db status`を専用利用者で明示実行する。
7. `catalog sync`と`ctrlcmd validate`を一度実行する。
8. Unitを配置して`daemon-reload`し、サービスをenableして起動する。
9. `systemctl status`と製品の状態表示で起動を確認する。

DB、catalog、channel mapのいずれかが失敗した場合は、unitを配置せずサービスを起動しない。

## 更新

1. 録画中でないことと、直近の予約に停止時間が重ならないことを確認する。
2. サービスを停止し、終了を確認する。
3. 旧版の`db backup`で手動バックアップを作り、表示されたbackup IDを記録する。
4. 新版を新しい版別ディレクトリへ展開し、その絶対パスの実行ファイルで`--version`を確認する。
5. 新版で`db status`を実行する。`BEHIND`の場合だけ`db migrate`を明示実行する。
6. `CURRENT`を確認してから実行ファイルとunitのリンクを新版へ切り替える。
7. `daemon-reload`後にサービスを起動し、起動状態を確認する。

環境設定ファイルは更新で上書きしない。古い標準例から作った環境設定に
`/etc/sazanami-dvr/channels.json`が残る場合は、起動前にチャンネル設定をdata root直下へ配置し、
`SAZANAMI_CHANNEL_MAP`を新しいパスへ直す。旧ファイルは新版の起動確認が終わるまで削除しない。

旧版ディレクトリと更新前バックアップも、新版の運転確認が終わるまで削除しない。

## 切り戻し

DB形式が変わっていない場合は、サービスを停止し、実行ファイルとunitのリンクを旧版へ戻す。旧版の
`db status`で`CURRENT`を確認してから起動する。

DB形式が変わった場合は、サービス停止後に新版の`db restore`で更新前バックアップを戻す。復元が`COMMITTED`に
なったことを確認し、旧版の`db status`が`CURRENT`になってからリンクを戻して起動する。復元が中断した
場合は通常起動せず、表示されたoperation IDで新版の`db recover`を行う。

どちらの場合も環境設定、チャンネル設定、録画ファイルを削除、移動、上書きしない。

## 通常削除とpurge

通常削除ではサービスを停止して無効化し、unit、実行リンク、版別配布物だけを削除する。次は残す。

- `/etc/sazanami-dvr/`以下の環境設定
- `/var/lib/sazanami-dvr/`以下のチャンネル設定、DB、バックアップ、既定録画先
- 別に指定した録画保存先
- `sazanami-dvr`利用者とgroup

Purgeは別の明示操作とする。サービスと配布物を通常削除した後、利用者が対象の絶対パスを一つずつ確認した
場合だけ、設定ディレクトリ、data root、別録画先、最後に専用利用者とgroupを削除する。Purge手順は`/`、
`/var`、`/home`、glob、未解決の環境変数を削除対象にしない。

## 必須テスト

- Unitの必須directive、単一`ExecStart`、migration不在を静的に確認する。
- Hosted Ubuntuで`systemd-analyze verify`を成功させる。
- 環境設定例のdata root、チャンネル設定、三つの既定portを確認する。
- 基準unitと環境設定例に同時録画数のflag、固定値、空値用変数がないことを確認する。
- Release workflowでamd64／arm64の両アーカイブへ公開文書、unit、設定例を収録する。
- 展開した各アーカイブで実行ファイルのversion、VCS revision、収録パスを確認する。
- Fresh Ubuntuで初回導入、更新、切り戻し、通常削除後のdata保持、明示purgeを一回確認する。
- Purgeは隔離した標準data rootだけで確認し、実運用dataへ実行しない。

時間指定の耐久試験、長期resource測定、利用者のアーカイブ照合は必須テストにしない。
製品コード、API、DB schema、録画処理を変更しないことも差分で確認する。
