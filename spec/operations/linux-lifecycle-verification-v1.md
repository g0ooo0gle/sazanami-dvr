# Linux lifecycle検証仕様 v1

- Status: Superseded
- Date: 2026-08-23
- Applies to: Sazanami DVR v0.1.2以降のLinux amd64 release archive
- Decision: `docs/adr/0068-two-layer-linux-lifecycle-verification.md`
- Base specification: `spec/operations/linux-installation-lifecycle-v2.md`
- Superseded by: `spec/operations/linux-lifecycle-verification-v2.md`

Fresh Ubuntuで短くlifecycleを一回確認する部分だけを引き継ぐ。Archive checksum、必須LAB、
厳しい証拠成果物は現在のrelease gateにしない。実装はHandoff 0062を正本とする。

## 目的

Linux導入・更新・削除仕様v2の一続きの操作を、fresh Ubuntuの標準配置と、実験環境の隔離profileで検証する。
本仕様は検証方法を固定するものであり、製品runtime、公開配置、DB schema、通常削除、purgeの意味を変更しない。

## 証拠区分

| 区分 | 確認する対象 | Provider | 代替できない証拠 |
|---|---|---|---|
| Standard CI | 標準利用者、標準path、標準unit、既定port、全lifecycle | Synthetic | LAB実接続 |
| Release archive | 公開直前のexact archiveとStandard CIの同一性 | Synthetic | LAB実接続 |
| LAB smoke | 同じcandidateの実Mirakurun接続と起動 | Real | 標準配置と全lifecycle |

三つの区分はexact製品commitとarchive SHA-256へ結び付ける。一つの成功を別区分の成功として扱わない。

## LVC-001: 旧版とcandidateを固定する

- 旧版は公開v0.1.2、製品commit `d2564b3b652a34ecec1310cc80331cbd4a89cc80`とする。
- Linux amd64 archiveの既知SHA-256は
  `5e8a27593e4ef3e43f4cf208c1161351b9c918ac56f794ba3236b707b2380662`とする。
- Candidateはworkflowが検証対象commitから作ったLinux amd64 archiveとし、完全commit SHAとSHA-256を記録する。
- Release workflowでは、検証済みcandidate archiveと公開対象archiveがbyte単位で同一でなければ公開しない。
- V0.1.2のschemaは12、現在のcandidateは13以降であることを製品commandから確認する。将来candidateがschema 13を
  超えても、旧版からの連続migrationとrestore条件は維持する。

## LVC-002: Fresh VMと標準resourceを使う

Standard CIは`ubuntu-24.04` x64を明示し、各run専用のfresh GitHub-hosted VMで実行する。`ubuntu-latest`と
container型の`ubuntu-slim`は使わない。次を標準値のまま使う。

| 対象 | 値 |
|---|---|
| 利用者／group | `sazanami-dvr` |
| 配布物 | `/opt/sazanami-dvr/<version>/` |
| 実行link | `/usr/local/bin/sazanami-dvr` |
| 設定 | `/etc/sazanami-dvr/` |
| Data root | `/var/lib/sazanami-dvr/` |
| Unit | `sazanami-dvr.service` |
| CtrlCmd | `0.0.0.0:4520` |
| 録画HTTP | `127.0.0.1:4521` |
| WebUI手動確認 | `127.0.0.1:4522` |

利用者、group、path、unit、portのいずれかが開始前から存在または使用中なら失敗する。既存resourceを再利用、上書き、
停止、削除してはならない。

## LVC-003: Synthetic providerを最小に保つ

Synthetic MirakurunはPython標準libraryの静的HTTP serverをloopbackだけで起動する。次のGETだけへ固定JSONを返す。

- `/api/version`
- `/api/services`
- `/api/programs`
- `/api/tuners`

Fixtureは架空の一service、一program、二tunerまでとし、実番組、実接続先、TS、credentialを含めない。Catalog同期後、
Python標準libraryのSQLite読取りでbackend IDを得て、現在のschemaに合う最小channel mapを生成する。Provider応答や
channel mapの内容をCI logへ出さない。

## LVC-004: V0.1.2を初回導入して起動する

1. 公開archiveとSHA-256を照合する。
2. Linux導入・更新・削除仕様v2の標準利用者、directory、owner、modeを作る。
3. V0.1.2を版別directoryへ展開し、実行link、環境設定、channel map、unitを配置する。
4. 専用利用者で`db status`、`db migrate`、`db status`、`catalog sync`、`ctrlcmd validate`を順に実行する。
5. Unitをenableして起動し、active状態、schema 12、4520／4521の待受、製品状態表示を確認する。
6. 4522はunitから待受しないことを確認する。手動WebUI smokeを行う場合だけloopbackで起動し、直後に停止する。

途中で失敗した場合はunitを起動せず、既存resourceを変更しない。

## LVC-005: Backupしてcandidateへ更新する

1. 録画中でないことを製品状態から確認し、serviceを停止する。
2. V0.1.2の`db backup`でmanual backupを作る。
3. Candidateを新しい版別directoryへ展開し、その絶対pathの`--version`と完全revisionを確認する。
4. Candidateの`db status`が`BEHIND`であることを確認し、`db migrate`を明示実行する。
5. `CURRENT`かつschema 13以降を確認してから実行linkとunitをcandidateへ切り替える。
6. Serviceを起動し、active状態、4520／4521の待受、製品状態表示を確認する。

環境設定、channel map、録画、backupを上書きまたは削除しない。

## LVC-006: 復元、切り戻し、再更新を行う

1. Candidate serviceを停止する。
2. Candidateの`db restore`でLVC-005の更新前backupを戻し、結果が`COMMITTED`であることを確認する。
3. V0.1.2の`db status`が`CURRENT`かつschema 12であることを確認する。
4. Linkとunitをv0.1.2へ戻して起動し、active状態と待受を確認する。
5. もう一度停止し、同じcandidateで明示migration、link切替、起動を行う。
6. Candidateが`CURRENT`かつschema 13以降で、serviceがactiveであることを確認する。

Restoreが中断した場合は通常起動せず、candidateの`db recover`を実行する。復旧できなければtestを失敗させ、purgeへ
進まない。

## LVC-007: 通常削除の保持を照合する

通常削除の前に、次のhash、件数、owner、modeを記録する。

- 環境設定とchannel map
- DBとSQLite sidecar
- Manual backup
- 既定録画directory内の合成sentinel file
- 専用利用者とgroup

Serviceを停止、disableし、unit、実行link、版別配布物だけを削除する。通常削除後、保持対象のhash、件数、owner、modeと
利用者／groupが変わっていないことを照合する。保持対象が一つでも変わった場合は失敗し、purgeへ進まない。

## LVC-008: Purgeを独立した明示操作にする

PurgeはLVC-007成功後の独立stepでだけ実行する。対象は
`/etc/sazanami-dvr`、`/var/lib/sazanami-dvr`、`sazanami-dvr`利用者、同groupに限定する。各pathが期待するowner、
mode、通常file／directory種別を満たすことを直前に確認する。

`/`、`/var`、`/home`、glob、未解決変数、symlink、別録画先を削除対象にしてはならない。一致しない対象があれば何も
削除せず失敗する。完了後、固定resourceが存在しないことだけを確認する。

## LVC-009: PR、Release、branch protectionへ結び付ける

- PR CIはStandard CIを必須jobとして実行する。
- Release workflowはarchive作成後、公開前に同じ検証入口をexact Linux amd64 archiveへ適用する。
- Standard CIとReleaseは同じscriptとfixtureを使い、引数でarchiveだけを切り替える。
- Releaseは検証済みarchiveのSHA-256と公開対象を照合してからpublishする。
- Mainでjobが成功し、check名を読み戻した後にだけbranch protectionのrequired checkへ追加する。
- Workflow変更、required check変更、公開結果はexact commitとrun IDでplanningへ記録する。

## LVC-010: LABは別名profileで実providerだけを確認する

LAB profileは次に固定する。

| 対象 | 値 |
|---|---|
| 利用者／group | `sazanami-lifecycle` |
| 配布物 | `/opt/sazanami-lifecycle/<version>/` |
| 実行link | `/usr/local/bin/sazanami-lifecycle` |
| 設定 | `/etc/sazanami-lifecycle/` |
| Data root | `/var/lib/sazanami-lifecycle/` |
| Unit | `sazanami-lifecycle.service` |
| CtrlCmd | `127.0.0.1:14520` |
| 録画HTTP | `127.0.0.1:14521` |
| WebUI | `127.0.0.1:14522` |

開始前に全専用resourceとportが空であること、稼働中Composeがhealthyであること、KonomiTVの予約件数をread-onlyで
確認する。専用resourceが一つでも存在する場合は、既存serviceを停止または変更せず、検証を即時失敗させる。

同じcandidate archiveを配置し、実Mirakurunへcatalog同期、tuner取得、channel map検査、専用unit起動を行う。録画、
予約、ライブ、KonomiTV操作は実行しない。終了後は当runで作成した専用unitと専用resourceだけを削除し、稼働中Composeの
healthと予約件数が開始前と一致することを確認する。実接続先、件数の生値、番組、予約を証拠へ残さない。

## LVC-011: READMEから四段階で初回起動へ進める

READMEは、次のtop-level sectionを順に置く。見出し名は自然な日本語へ調整できるが、役割と順序は変えない。

1. 製品説明と現在版
2. 最短セットアップ
3. 主な機能
4. 使用時の注意
5. 目的別ガイド
6. 開発
7. License

最短セットアップでは、利用者が次の四段階へ進めるようにする。

1. MirakurunまたはmirakcのURLと、対応する`channels.json`を準備する。
2. 同じGitHub ReleaseのLinux archiveと`SHA256SUMS`を取得し、hashを照合する。
3. 標準pathへ配置し、環境設定、DB、catalog、channel mapを準備する。
4. systemd serviceを起動し、active状態と4520／4521の待受を確認する。

READMEには各段階の短い説明と、初回に必要なcommandへ直接進めるlinkを置く。主な機能は利用目的ごとにまとめ、
command flag、対応version、検証履歴、実装内部の列挙へ広げない。詳細な初回導入、更新、切り戻し、
通常削除、purgeは`docs/linux-installation.md`を正本とし、同じ手順を別のquickstart fileへ複製しない。

READMEの既存「主な変更履歴」は、内容を失わずrepository rootの`CHANGELOG.md`へ移す。READMEには現在版の短い要約と
`CHANGELOG.md`へのlinkだけを残す。互換範囲と既知制限は`docs/compatibility.md`、安全な更新と削除は
`docs/linux-installation.md`を引き続き正本とする。Hash不一致時の停止、CtrlCmdをinternetへ直接公開しない注意、
更新前backup、purgeの不可逆性は、利用者が操作する場所から外さない。

READMEの「目的別ガイド」は、少なくとも次へ一回ずつ到達できるようにする。

- `docs/linux-installation.md`: 初回導入、更新、切り戻し、通常削除、purge
- `docs/konomitv-setup.md`: KonomiTVとの接続
- `docs/docker-compose.md`: Compose導入
- `docs/compatibility.md`: 対応範囲と既知制限
- `docs/recording-operations.md`: 予約と録画の運用
- `docs/catalog-database-operations.md`: DB、backup、restore、recover
- `CHANGELOG.md`: 版ごとの変更

READMEと変更した公開文書は、natural-japaneseのfull工程で確認する。機械検査はlint、outline、termsを使い、構造、
読みやすさ、guide適合を独立reviewする。公開面は現在の利用者が取る行動から書き、版別詳細、変更理由、削除済みの案を
READMEへ戻さない。`CHANGELOG.md`、安全表示、互換制限、復旧手順は履歴とリスクを記録する面なので省略しない。

Release archiveはREADME、`CHANGELOG.md`、`docs/linux-installation.md`を収録する。CIは三文書の存在、相互link、
標準path、既定port、archive収録を確認する。

## 必須の失敗条件

- Resourceまたはportが開始前から使用中である。
- 旧版またはcandidate archiveのSHA-256、版、revisionが期待値と異なる。
- DB状態、migration、backup、restore、recoverの結果を一意に確認できない。
- Service停止、systemd active、待受、保持対象のhash／件数を確認できない。
- 通常削除後に保持対象が変わる。
- Purge直前のpath、owner、mode、種別が期待値と異なる。
- LABの稼働中環境が開始前後で変わる。
- READMEの最短セットアップが四段階でない、または`CHANGELOG.md`と詳細Linux手順へ到達できない。
- Release archiveにREADME、`CHANGELOG.md`、`docs/linux-installation.md`のいずれかがない。

失敗時は後続stepを続けず、匿名化した段階、exit code、状態だけを記録する。

## 対象外と未実施表示

- 実際のreboot、待機、休止、電源断: `NOT RUN: host recovery is not guaranteed`
- Lifecycle検証中の実放送録画とTS保存: `NOT RUN: lifecycle scope excludes live recording`
- KonomiTVの機能再検証: `NOT RUN: covered by the v0.5.0 practical backend evidence`
- Arm64のsystemd実起動: `NOT RUN: hosted arm64 lifecycle runner is not part of v1`

Linux arm64 archiveは既存Release検証どおり、版、revision、CGO無効、収録path、SHA-256を確認する。
