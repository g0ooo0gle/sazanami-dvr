# 簡単セットアップ仕様 v1

- Status: Accepted
- Date: 2026-08-24
- Applies to: Sazanami DVR v1.1.0以降
- Decision: `docs/adr/0070-streamlined-installation-entrypoints.md`
- Related specifications: Linux導入・更新・削除仕様v3、Docker Compose導入仕様v1、製品版とリリース準備の仕様v1
- Requirements: `SETUP-001`～`SETUP-012`
- Precedence: 本仕様はinstallerの三操作だけを追加する。標準外録画先を個別確認後に手動削除する
  Linux導入・更新・削除仕様v3の手順は置き換えない

## 目的

READMEからsystemd版とDocker Compose版のどちらかを選び、詳細を読み切らなくても初回起動に必要な操作を
追えるようにする。systemd版は配布アーカイブ内の一つのshellで標準配置を作り、通常削除と全消去を安全に
分ける。

## 共通境界

- Mirakurun／mirakc、チューナー、driver、cardは導入対象に含めない。
- 実環境のURL、token、番組、録画、DB、秘密を配布物、test、logへ含めない。
- DB migration、catalog sync、channel map検証は、結果を確認できる明示操作とする。
- 公開済みtagと配布物を移動、差替え、再利用しない。
- 未確認のKonomiTV、Komorebi、EDCB機能を、導入方法や版番号だけで対応済みにしない。

## Shell installer

### `SETUP-001`: 配布と実行環境

Linux amd64／arm64のrelease archiveへ`packaging/install.sh`をmode 0755で収録する。POSIX `sh`を使い、
製品のGo依存、外部package、network downloadを追加しない。対象はrootでsystemdを使うLinuxとする。

### `SETUP-002`: 操作を三つに限定する

第一引数は`install`、`uninstall`、`purge`のいずれかだけを受け付ける。引数なし、未知の操作、余分なpath引数は
usageを表示して変更前に失敗する。削除対象を引数や環境変数で広げない。

### `SETUP-003`: 配布元を検査する

`install`は`packaging`directoryの親にある配布rootを使う。少なくとも実行可能な通常fileの`sazanami-dvr`、通常fileのsystemd unit、
通常fileの環境設定例を必要とする。Symlink、特殊file、複数rootを経由せず、binaryの`--version`が厳密な三桁版を
返すことを確認する。

### `SETUP-004`: 標準配置を作る

初回`install`は次を行う。

1. OSの乱数源から128 bit以上の識別子を作り、`sazanami-dvr`専用system userと同名groupを作る。既存なら
   管理markerを含む全条件が一致する場合だけ再利用する。
2. `/opt/sazanami-dvr/<version>`、`/etc/sazanami-dvr`、`/var/lib/sazanami-dvr`、既定録画directoryを作る。
3. 実行file、installer、systemd資材、license、README、CHANGELOG、Linux／Compose導入文書を版別directoryへ
   root所有で配置する。
4. `/usr/local/bin/sazanami-dvr`と`/etc/systemd/system/sazanami-dvr.service`を同じ版へ向ける。
5. `/etc/sazanami-dvr/sazanami-dvr.env`がない場合だけ、環境設定例からmode 0600で作る。
6. `systemctl daemon-reload`を実行し、次の明示準備を表示して終了する。

設定、channel map、DB、backup、録画が既にある場合は上書き、移動、削除しない。同じ版への再実行は、管理対象と
一致する場合だけ安全に完了し、既存内容を変えない。

識別子はLinuxの`/dev/urandom`から16 byte以上を読み、`od`と`tr`で32文字以上の小文字16進数へ変換する。
Installerが専用利用者を新規作成する場合は、commentを`sazanami-dvr-installer-<識別子>`に固定する。
`/etc/sazanami-dvr/.installer-managed-account`は、形式版、同じ識別子、作成後の10進UID、10進primary GIDを
一項目ずつ記録したroot所有、mode 0600、hard link数1の通常fileとする。

既存利用者を再利用する場合は、markerの形式、識別子、UID、GIDが現在のaccountと一致し、commentが
`sazanami-dvr-installer-<識別子>`、homeが`/var/lib/sazanami-dvr`、login shellが`/usr/sbin/nologin`、
primary groupが`sazanami-dvr`でなければならない。同名groupに補助memberや同じGIDをprimary groupにする他利用者が
いる場合も再利用しない。Markerがない場合や一項目でも異なるaccountとgroupは自動修復、再利用、削除しない。

新規accountまたはgroupを作った後に`install`が失敗した場合も、同じ識別子、UID、GID、commentと標準属性が
現在値に完全一致する場合だけ、その実行で作ったaccountとgroupを片付ける。一致しなければ残して固定理由を表示する。

管理markerだけが残った後、元の識別子を引き継がずに同名accountを作り直した場合は再利用も削除も拒否する。
元の識別子を読み、UID、GID、commentまで意図的に再現できるrootは区別しない。Rootは固定対象を直接削除できるため、
悪意あるrootからの保護は本scriptの対象外とする。

`/opt/sazanami-dvr/.installer-managed-runtime`にも固定識別子と導入版を記録し、root所有、mode 0600にする。
通常削除はこのmarkerと実際のlink targetが示す一つの版だけを管理対象とする。

### `SETUP-005`: 自動準備を行わない

Installerは次を実行しない。

- `db migrate`、`db restore`、`db recover`
- `catalog sync`
- channel mapの生成、置換
- サービスのenable、start、restart
- OS packageの導入、更新
- release archiveやcontainer imageのdownload

利用者は配置後、DB状態、必要なmigration、catalog sync、channel map検証を順に実行し、成功した場合だけサービスを
enable／startする。

### `SETUP-006`: 管理外資源を変更しない

標準pathがsymlink、期待しないfile種別、管理外のbinary／unit link、矛盾した専用利用者・groupの場合は、変更前に
固定理由で失敗する。Installerは管理外資源を上書き、移動、削除、自動修復しない。

### `SETUP-007`: 通常削除でdataを残す

`install`、`uninstall`、`purge`は、サービスが稼働中なら変更前に失敗する。利用者は録画中でないことを確認し、
サービスを明示停止してから削除操作へ進む。

`uninstall`は管理対象linkを検査し、停止済みのサービスを無効化した後、unit link、binary link、
`/opt/sazanami-dvr`だけを削除する。次は属性と内容を変えずに残す。

- `/etc/sazanami-dvr`
- `/var/lib/sazanami-dvr`
- 標準内外の録画
- DBとbackup
- `sazanami-dvr`利用者とgroup

管理対象外のlinkやfileがあれば、サービスを止める前に失敗する。

`/opt/sazanami-dvr`配下は`SETUP-004`で列挙したfile、必要な親directory、installerの固定管理markerだけを認める。
未知file、未知directory、symlink、特殊file、管理外ownerがあれば削除しない。通常削除は認めたfileを個別に削除し、
空になったdirectoryだけを`rmdir`する。`rm -r`と`rm -rf`は使わない。

### `SETUP-008`: Purgeを分離する

`purge`は二回のmutation-free preflightを行う。一回目は確認表示の前、二回目は`PURGE`の完全一致を受けた直後に行う。
各回で、稼働中service、`SETUP-007`の通常削除対象、専用accountとgroup、実行中process、環境設定、録画先の包含関係、
固定pathの祖先、対象root、全配下を検査する。二回とも完了するまで`systemctl disable`、linkやfileの削除、`rmdir`、
再帰削除、`userdel`、`groupdel`を実行しない。一項目でも不一致があれば、通常削除を含む変更は0件とする。

一回目のpreflight後、削除する固定対象と、標準data外の録画先を残す旨を表示する。録画先のpath値は表示しない。
標準入力から`PURGE`の完全一致を受け、二回目のpreflightにも成功した場合だけ、通常削除と次の固定対象の削除を始める。

- `/etc/sazanami-dvr`
- `/var/lib/sazanami-dvr`
- `sazanami-dvr`利用者とgroup

確認不一致またはEOFでは変更前に失敗する。標準外の録画先、`/`、`/var`、`/home`、glob、未解決の環境変数、
任意pathは削除しない。

固定pathの祖先`/opt`、`/etc`、`/var`、`/var/lib`と、`/opt/sazanami-dvr`、`/etc/sazanami-dvr`、
`/var/lib/sazanami-dvr`の対象rootを`lstat`相当で一つずつ検査する。いずれもsymlinkではない通常directoryとし、
`/`以外のmountpointを一つも認めない。祖先はUID 0所有で、groupまたはotherの書込bitがないことも必要とする。
欠落、別owner、group／other書込可能、symlink、通常directory以外、mountpointの場合は変更前に失敗する。

全変更の前に、`/etc/sazanami-dvr/sazanami-dvr.env`がroot所有、hard link数1の通常fileであり、symlinkではないことを
確認する。その後もfileをsourceせず、`SAZANAMI_DATA_ROOT=`と`SAZANAMI_RECORDING_ROOT=`で始まる代入を各一行だけ
読む。値は展開せず、削除対象には使わない。どちらかの欠落や重複、相対path、空値、空白、引用符、backslash、`$`、
backtick、glob文字、空segment、`.`または`..`のsegmentを含む値は拒否する。Data rootは
`/var/lib/sazanami-dvr`との完全一致を必要とする。

録画先が`/var/lib/sazanami-dvr/recordings`なら、既定録画先として標準dataとともに削除する。既定値以外では、`/`を
除く録画先までの全path componentが存在する通常directoryで、symlinkでもmountpointでもないことを検査する。実体pathを
解決し、`/var/lib/sazanami-dvr`と同じ、またはそのpath境界上の配下ならpurge全体を変更前に拒否する。
`/var/lib/sazanami-dvr2`のような共通prefixは配下とみなさない。存在しないpath、symlink、bind mountを含むmount alias、
実体を安全に解決できないpathも拒否する。標準data外と確認できた通常directoryは内容を読まずに残し、値を画面や
logへ表示しない。

専用利用者を削除する前に、管理markerの形式、識別子、UID、GID、現在のaccountのcomment、home、login shell、
primary groupが`SETUP-004`の標準形と一致し、同利用者のprocessがないことを確認する。同名groupには補助memberがなく、
同じGIDをprimary groupにする他利用者もいないことを確認する。どれかが不一致なら、通常削除を含む全変更の前に
失敗する。

三つの対象rootの全階層は、rootまたは専用利用者が所有する通常fileとdirectoryだけを認める。
Symlink、socket、FIFO、device、複数hard linkの通常file、別owner、対象root自身を含むmountpointが一つでもあれば、
通常削除を含む全変更の前に失敗する。`/opt/sazanami-dvr`は`SETUP-007`の個別削除だけを使う。この検査を通った
`/etc/sazanami-dvr`と`/var/lib/sazanami-dvr`だけを再帰削除できる。

標準外録画先の削除は本scriptの外とする。Linux導入・更新・削除仕様v3に従い、利用者が絶対pathと内容を個別確認して
手動で行う場合だけ削除できる。別partition、外部録画disk、mount aliasが理由で自動purgeを拒否した場合も、scriptは
unmountやpathの変更を行わず、同じ手動手順へ固定文で案内する。

### `SETUP-009`: 失敗を固定理由で示す

失敗は非0で終了し、少なくともroot不足、Linux／systemd不足、配布元不正、version不正、既存資源、管理外link、
purge確認不一致を短い固定理由で区別する。接続先やdata内容を表示しない。

## Docker Compose入口

### `SETUP-010`: 既存構成を再利用する

Composeの最短手順は、配布archiveの`packaging/compose`を専用directoryへ置き、既存の`prepare.sh`、`.env`、
KonomiTV設定例、Compose manifestを使う。別のwrapper、host root mount、Docker socket、privileged、不要なdeviceを
追加しない。

### `SETUP-011`: 起動までを先に示す

Compose詳細手順の先頭で、次の順を一続きに示す。

1. `./prepare.sh`
2. `.env`と`config/konomitv.yaml`へ同じMirakurun URLを設定する。
3. `data/sazanami/channels.json`を配置する。
4. `docker compose build konomitv`
5. `db status`、必要な`db migrate`、再度の`db status`、`catalog sync`、`ctrlcmd validate`
6. `docker compose up -d`と`docker compose ps`

いずれかが失敗した場合は通常serviceを起動しない。停止、更新、切り戻し、録画削除、制限は後続章へ分ける。

## 公開文書と版

### `SETUP-012`: 二つの入口をREADMEへ置く

READMEの最短セットアップへ「systemdへ入れる」と「Docker ComposeでKonomiTVと起動する」を並べ、それぞれの短い
コマンドと詳細文書へのlinkを示す。長い変更履歴、詳細な安全説明、更新、切り戻しは目的別文書へ分ける。

本機能はv1.1.0で公開する。機能PRでは版を変えず、統合後の専用PRで製品版、版表示test、README、CHANGELOG、
Compose既定image、tag、release資産をそろえる。

## 必須テスト

- Shell syntaxと未知操作の失敗。
- 配布元の欠落、symlink、特殊file、version不一致の拒否。
- Fresh Ubuntuでの初回installと同版再実行。
- Install後にDB、catalog、channel map、サービスが自動変更されていないこと。
- 管理外binary／unit link、稼働中serviceの変更前拒否。
- 管理markerがない既存account、識別子、UID、GID、commentの不一致、元の識別子を引き継がずに同名accountを
  作り直した場合、groupの共同利用、`/opt`配下の未知fileに対する変更前拒否。
- Uninstall後の設定、channel map、DB、backup、録画、利用者、groupの完全保持。
- Purge確認不一致、account属性不一致、実行中process、symlink、特殊file、別owner、mountpointがある場合の変更0件と、
  確認一致後の固定標準path、利用者、groupの削除。
- 確認待ちの間にaccount、link、環境設定、固定path treeを不正な状態へ差し替えた場合の二回目preflight拒否と、
  `/opt`を含む変更0件。
- 既定録画先の削除、標準dataと同じか配下にある非標準録画先、外部symlink／mount aliasの変更前拒否、
  標準data外の実directoryの完全保持。
- 固定pathの祖先、対象root、配下のsymlink、非directory、mountpointに対する変更前拒否。
- 固定pathの祖先がroot以外の所有、またはgroup／other書込可能な場合の変更前拒否。
- 環境設定がsymlink、管理外owner、複数hard linkの場合と、path代入が欠落、重複、相対指定、dot segment、
  shell展開を含む場合の変更前拒否。
- amd64／arm64 archive内のinstaller、mode、公開文書、version、VCS revision。
- 既存のsystemd、Compose、Linux lifecycle、通常Go品質test。
- READMEと二つの詳細手順のlink、コマンド、現在版の整合。

## 完了条件

製品mainの完全SHAで必須testが成功し、installerを含むLinux amd64／arm64 archive、v1.1.0 tag、GitHub Release、
OCI imageを同じrelease commitから読み戻す。公開文書は実装済みの三操作とCompose既存構成だけを案内し、
未確認の互換範囲を増やさない。
