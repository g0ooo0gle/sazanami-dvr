# ADR-0070: 配布アーカイブへ小さな導入・削除スクリプトを同梱する

- Status: Accepted
- Proposed date: 2026-08-24
- Decision date: 2026-08-24
- Deciders: プロジェクトオーナー
- Reviewer: Codex
- Related: Plan 0087、ADR-0051、ADR-0053、ADR-0062、ADR-0069
- Product copy path: `docs/adr/0070-streamlined-installation-entrypoints.md`
- Product sync state: NOT COPIED
- Partially supersedes: ADR-0051の独自installerを不要とする判断
- Operational precedence: 簡単セットアップ仕様v1は、installerが標準外録画先を削除しない範囲だけ
  Linux導入・更新・削除仕様v3を補足する。V3の手動確認後の別削除は置き換えない
- Superseded by: None

## 背景

v1.0.0はsystemd向けの標準配置、更新、切り戻し、通常削除、purgeの詳細手順を持つ。Docker Compose構成も
Sazanami DVRと固定KonomiTVを分離し、永続dataをhostへ残す形で提供済みである。

安全な手順はそろったが、systemd版の初回導入では、専用利用者、directory、archive展開、実行fileとunitのlink、
環境設定を個別に作る。Compose版も起動までの操作が詳細説明に分かれている。プロジェクトオーナーは、完成版へ
簡単なinstallerとComposeの最小起動手順を加え、installerから削除も行えるようにすることを明示した。

## 判断

Linux amd64／arm64の配布アーカイブへ、POSIX `sh`で動く`packaging/install.sh`を実行可能fileとして同梱する。
外部dependency、package repository、常駐処理は追加しない。

Scriptの操作は次の三つに限定する。

| 操作 | 役割 | 残すもの |
|---|---|---|
| `install` | 標準利用者、directory、版別配布物、link、初回環境設定を作る | 既存の設定とdataを上書きしない |
| `uninstall` | サービス、管理対象link、版別配布物を削除する | 設定、DB、backup、録画、利用者、group |
| `purge` | 確認後に通常削除と標準の設定・data・専用利用者削除を行う | 標準外の録画先 |

`install`はroot、Linux、systemd、配布元の通常file、製品版、標準pathを事前に確認する。既存資源がsymlink、
特殊file、管理外link、矛盾した利用者・groupの場合は、変更前に停止する。版別directoryへ配布物を置き、
`/usr/local/bin/sazanami-dvr`とsystemd unitを同じ版へ向ける。環境設定は存在しない場合だけ例から作る。
Installerは専用利用者を作る前にOSの乱数源から128 bit以上の識別子を生成し、同じ識別子をaccountのcommentと
root所有の管理markerへ記録する。Markerには形式版、識別子、作成時のUID、primary GIDを含める。既存の専用利用者を
再利用する場合は、markerの識別子、UID、GIDと現在のaccountが一致し、comment、home、login shell、primary groupも
標準形と一致しなければならない。Markerがない場合や一項目でも異なる場合は変更しない。

この識別子は、markerだけが残った状態で同名accountを通常の手順で作り直した場合の誤認を防ぐ。Rootが元の識別子を
読み、UID、GID、commentまで意図的に再現する場合は区別しない。Rootは固定削除対象を直接変更できるため、悪意ある
rootからの保護は本installerの境界外とする。

版別directoryへは実行file、installer、systemd資材、license、README、CHANGELOG、二つの導入文書だけを置き、
導入版を含むroot所有のruntime markerを`/opt/sazanami-dvr`直下へ残す。
Installerが把握していないfileやdirectoryが`/opt/sazanami-dvr`配下にあれば通常削除を拒否する。通常削除は既知fileを
一つずつ削除し、空になったdirectoryを`rmdir`する。`/opt/sazanami-dvr`へ再帰削除を使わない。

InstallerはDB migration、catalog sync、channel map生成、サービスのenable／startを実行しない。配置後に必要な
明示操作を表示し、利用者が各結果を確認する。既存更新と切り戻しの手順も自動化せず、詳細文書へ残す。

`install`、`uninstall`、`purge`は、サービスが稼働中なら変更前に終了する。利用者は録画中でないことを確認して
サービスを明示停止してから削除操作を行う。

`uninstall`は管理対象のlinkだけを削除する。管理対象外のfileやlinkを見つけた場合は、fileを変更せず終了する。
通常削除後も`/etc/sazanami-dvr`、`/var/lib/sazanami-dvr`、専用利用者とgroupを残す。

`purge`は、通常削除を含む全preflightを変更なしで完了してから、削除する固定pathと標準data外の録画先を残す旨を
表示する。`PURGE`の完全一致後に同じpreflightを再実行し、すべての条件に再び一致した場合だけ削除を始める。対象は
`/opt/sazanami-dvr`、`/etc/sazanami-dvr`、`/var/lib/sazanami-dvr`、専用利用者とgroupに限定する。
任意path引数、glob、未解決の環境変数、広い親directoryを受け付けない。専用利用者とgroupは、識別子、UID、GID、
commentを含む標準account属性と管理markerが一致し、実行中process、groupの補助member、同じgroupをprimary groupに
する他利用者がない場合だけ削除する。

固定pathの祖先である`/opt`、`/etc`、`/var`、`/var/lib`、三つの対象root、全配下を検査する。祖先はroot所有かつ
group／other書込不可、祖先と対象rootはsymlinkではない通常directoryとし、`/`以外のmountpointを認めない。
配下にもsymlink、特殊file、rootまたは
専用利用者以外のowner、nested mountを認めない。一つでも不一致なら、通常削除を含む全対象を変更前に拒否する。

包含検査のため、Installerは`/etc/sazanami-dvr/sazanami-dvr.env`がroot所有、hard link数1の通常fileであることを
確かめてから、`SAZANAMI_DATA_ROOT`と`SAZANAMI_RECORDING_ROOT`のliteral代入だけをsourceも展開もせずに読む。
この値を削除対象には使わない。
Data rootは`/var/lib/sazanami-dvr`との完全一致を必要とする。録画先が既定の
`/var/lib/sazanami-dvr/recordings`なら標準dataとともに削除する。既定値以外の録画先は、すべてのpath componentが
symlinkではない通常directoryで、`/`以外のmountpointを含まず、実体pathも標準data外である場合だけ残せる。
存在しないpath、symlink、mount alias、標準dataと同じ実体またはその配下はpurge全体を変更前に拒否する。
代入の欠落、重複、相対path、`.`や`..`のsegment、shell展開を含む値も変更前に拒否し、録画先の値や内容を表示しない。

Linux導入・更新・削除仕様v3にある、利用者が絶対pathを個別確認した標準外録画先の手動削除は残す。
本installerはそのpathを包含検査にだけ使い、purgeの削除対象にしない。
別partition、外部録画disk、mount aliasが理由で自動purgeを拒否した場合も、Installerはunmountせず、同じ手動手順へ
固定文で案内する。

READMEはsystemd installerとDocker Composeを同じ高さの入口として示す。Linux詳細手順はinstallerによる初回配置を
先に示し、手動導入、更新、切り戻し、安全境界を後ろへ残す。Compose詳細手順は既存の`prepare.sh`、設定編集、
channel map配置、KonomiTV build、明示DB準備、`docker compose up -d`を先頭へまとめる。Composeの実装は変えない。

本変更は利用者向け機能なので、公開済みv1.0.0を変えず、次の機能版v1.1.0で提供する。機能実装と版更新は
ADR-0053に従って別PRにする。

## 理由

単一のshellなら、既存のrelease archive、標準path、systemd unitをそのまま使い、手入力が多いOS配置だけを短くできる。
DBと接続設定を明示操作のまま残せば、導入の手数を減らしても更新・復旧の判断を隠さない。

通常削除とpurgeを別操作にすれば、再導入や障害調査に必要なdataを守りながら、不要になった環境の全消去も選べる。
Composeは既に再現可能な構成を持つため、新しいwrapperより文書の入口を整える方が小さい。

## 影響

- systemd版の初回OS配置は一つのコマンドになる。
- DB準備とサービス起動には、従来どおり明示コマンドが必要になる。
- 通常削除後は同じ設定とdataで再導入できる。
- Purgeは元に戻せないため、完全一致の確認が必要になる。
- 標準外の録画先は安全のため残り、利用者が別に確認する。
- Release workflowとFresh Ubuntu lifecycleはinstallerの収録と三操作を確認する。
- v1.1.0は実行時API、DB、録画処理を変えない。

## 採用しなかった案

### `.deb`とAPT repositoryを同時に提供する

署名、repository、maintainer script、依存、更新と切り戻しの運用が増える。現在の単一binaryには過大なため採用しない。

### InstallerでDB migrationとサービス起動まで行う

DB状態、接続先、channel mapの失敗を一つのroot操作へ隠すため採用しない。

### 通常削除で全dataを削除する

録画と復旧材料を意図せず失うため採用しない。全消去は確認付き`purge`へ分離する。

### 標準外の録画先を環境設定から読み取って削除する

設定fileの差替え、symlink、誤記によって削除範囲が広がるため採用しない。

### Compose用wrapperを追加する

既存の明示コマンドを短い順序で示せば足り、運用入口を増やすため採用しない。

## 見直す条件

- 署名付きpackage repositoryを継続運用する体制ができた。
- systemd以外のLinux環境を正式対象にする。
- Installerで安全に判定できる更新・切り戻し契約を別に採用する。
- Composeの固定KonomiTV imageやnetwork境界が変わる。

## Product synchronization

- Handoff: Handoff 0064
- Planning source commit: 未確定
- Target product base commit: `511aa348db236a69c1adefb2c8d1a96e821893a0`
- Product destination: 同じpath
- Last synchronized product commit: None
- Known divergence: 製品側に本ADRはまだない
