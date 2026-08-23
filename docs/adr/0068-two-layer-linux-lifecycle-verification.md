# ADR-0068: Linux lifecycle検証を標準配置CIと隔離LABの二層に分ける

- Status: Accepted
- Proposed date: 2026-08-23
- Decision date: 2026-08-23
- Owners: Project owner
- Decision reviewers: Project owner（明示承認）、Codex
- Related requirements: Linux導入・更新・削除仕様v2
- Related planning documents: Plan 0066、Plan 0085
- Related handoffs: Handoffs 0041、0049
- Product copy path: `docs/adr/0068-two-layer-linux-lifecycle-verification.md`
- Product sync state: COPIED (SUPERSEDED REVISION; ADR-0069と仕様v2はNOT COPIED)
- Supersedes: None
- Partially superseded by: ADR-0069（archive checksum、必須LAB証拠、厳しい成果物をrelease gateにする部分）

## Context

Linux導入・更新・削除仕様v2は、標準配置、systemd、明示migration、backup、切り戻し、通常削除、purgeを
定めている。公開archiveと静的CIは存在するが、標準配置を使って一続きにserviceを起動したUbuntu証拠がない。

実験環境には稼働中Composeと古い標準導入がある。同じhostで公開手順どおりの標準配置を再現すると、利用者、path、
unit、portが衝突する。実運用環境を止めて試験することも、privileged containerの中へsystemdを新設することも、
今回の残件を閉じるための必要条件ではない。

必要なのは、公開手順を安全に再現できることである。本ADRは当初、実Mirakurun接続も別層の必須証拠とした。
ADR-0069はその必須化を置き換え、provider接続に影響する変更がある場合だけLABを使う。

## Decision drivers

- 公開手順と標準配置を正確に再現できること
- 稼働中環境、録画、予約、実データを変更しないこと
- 公開旧版からcandidateへの実migrationとrollbackを確認できること
- PRとReleaseで同じ検証を繰り返せること
- 実Mirakurun接続の確認をsynthetic providerで代用しないこと
- Privileged containerや新しい依存を増やさないこと

## Options

| 案 | 標準配置の再現 | 実provider | 稼働中環境の危険 | 反復性 | 判断 |
|---|---|---|---|---|---|
| Fresh Ubuntu CIだけ | 完全 | なし | 低い | 高い | 実接続証拠が不足 |
| 実験hostだけ | 衝突により不完全 | あり | 高い | 低い | 採用しない |
| Privileged container + LAB | Container差分が残る | あり | 中程度 | 中程度 | 複雑さが不要 |
| Fresh Ubuntu CI + 隔離LAB | 完全 | あり | 低い | 高い | 推奨 |

## Decision

プロジェクトオーナーの2026-08-23の明示承認により、fresh Ubuntu CIを標準検証に採用する。

第一層では、`ubuntu-24.04` x64のfresh GitHub-hosted VMへ標準利用者、標準path、標準unit、既定portを作る。固定した公開
v0.1.2 archiveを導入し、Python標準libraryだけのsynthetic Mirakurunで初回起動する。Workflowが作ったexact
candidate archiveへ更新してschema 12から13以降へ進め、更新前backupから復元してv0.1.2へ切り戻し、再び
candidateへ更新する。通常削除の保持と明示purgeまで同じ入口で検証する。

第二層の隔離LABは、provider接続や実Mirakurun固有の境界を変更した場合に限り使う。
実施するときは、別名の利用者、path、unit、loopback portを持つ隔離profileで、同じcandidateの
実Mirakurunのcatalog、tuner取得、service起動だけを確認する。稼働中Compose、既存標準導入、KonomiTV、録画、予約には触れない。

標準配置CIとReleaseでのarchive検証を必須証拠とする。LABを実施した場合は別の証拠区分に記録し、
標準配置CIの代替にしない。

## Consequences

### Positive

- 公開手順のpath、利用者、unit、port、systemd起動をfresh VMで厳密に検証できる。
- v0.1.2からcandidateへの実migration、restore、rollback、再更新を反復できる。
- 実験環境の稼働中構成を維持したまま、実provider接続だけを補足できる。
- PRとReleaseが同じ検証入口を使い、archiveの取り違えを検出できる。

### Negative

- Synthetic providerを保守する。LAB profileは必要な変更の検証時だけ用意する。
- Fresh CIはroot権限とsystemd起動を伴い、通常のGo testより実行時間が長い。
- LAB smokeが必要な変更は、公開CIだけでは完結しない。

### Risks and mitigations

- Cleanupの誤り: 固定absolute pathだけを対象にし、resource存在時は開始前に失敗させる。
- Candidate archiveの取り違え: 版、VCS revision、OS／architecture、収録ファイルを検証する。
- Synthetic providerの過剰実装: `/api/version`、`/api/services`、`/api/programs`、`/api/tuners`の最小静的応答に限定する。
- LABへの影響: 別名resourceとloopback portを使い、開始前後に稼働中環境をread-onlyで照合する。

## Verification

- Accepted仕様v2のLVC2-001からLVC2-008を製品test、workflow、公開文書へ対応付ける。
- GitHub公式資料で、`ubuntu-24.04`がjobごとのVMでありpasswordless `sudo`を使えることを実装時にも確認する。
- PR CIのfresh Ubuntuで初回導入、更新、restore、rollback、再更新、通常削除、purgeを成功させる。
- Release workflowで公開archiveの版、VCS revision、OS／architecture、収録fileを確認する。
- Mainでも同じlifecycle checkが成功したことをread backする。
- Provider接続を変更した場合だけ、LAB隔離profileで実Mirakurunのcatalog、tuner取得、service起動を確認する。
- 実際のreboot、待機、休止、電源断は`NOT RUN: host recovery is not guaranteed`と記録する。

## Product synchronization

- Handoff: Handoff 0062がHandoff 0061を置き換える。
- Planning source commit: Handoff 0062で固定する。
- Target product base commit: Handoff 0062で固定する。
- Product destination: 同じpath。
- Last synchronized product commit: `822df1a42b1dbfe948c72ab20287c0d122e69dc2`（旧revision）
- Known divergence: 製品には本ADRの旧revisionと検証仕様v1があり、ADR-0069と検証仕様v2は未同期。

## Revisit when

- GitHub-hosted Ubuntuでsystemdを起動できなくなったとき。
- 標準配置、DB migration、backup形式、service managerが変わったとき。
- LABを使わず実providerを安全に検証できる専用runnerが用意されたとき。
- `.deb`またはAPT repositoryを正式な導入経路として採用するとき。
