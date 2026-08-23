# ADR-0069: 長時間試験と利用者向けチェックサム照合を公開条件にしない

- Status: Accepted
- Proposed date: 2026-08-23
- Decision date: 2026-08-23
- Owners: プロジェクトオーナー
- Decision reviewers: プロジェクトオーナー（明示判断）、Codex
- Related requirements: 実用リリース品質仕様v1、Linux導入・更新・削除仕様v3、Linux lifecycle検証仕様v2
- Related planning documents: Plan 0040、Plan 0085、Plan 0086
- Related handoffs: Handoffs 0061、0062
- Product copy path: `docs/adr/0069-practical-release-quality.md`
- Product sync state: NOT COPIED
- Supersedes: ADR-0053のv0.9.0経由と時間指定耐久試験、ADR-0067のchecksum必須成果物、ADR-0068のarchive checksum必須検証
- Superseded by: None

## Context

固定KonomiTV v0.14.1の日常導線は、製品testと実験環境で一通り確認され、v0.5.0として公開済みである。
次の安定版に必要なのは、通常の回帰test、導入しやすい公開文書、短いLinux lifecycle確認、版と実装の一致である。

古いrelease方針には、v0.9.0を経由し、最終候補を一定時間動かす条件が残っている。新しいLinux lifecycle判断には、
公開archiveと`SHA256SUMS`を照合し、その結果を必須証拠にする条件がある。いずれも、現在の利用者目標に対して
保守と完了判定を重くしている。

プロジェクトオーナーは2026-08-23、checksum照合を製品機能として不要とし、時間を固定した試験も、文書、機能、
品質目標、成果物に不要と明示した。この明示判断を採用する。

## Decision drivers

- 固定KonomiTVの日常利用へ必要な品質だけを確認すること
- 導入と更新を短時間で繰り返し確認できること
- 利用者向け手順とrelease成果物を簡潔にすること
- 既存のデータ安全境界を弱めないこと
- 未確認範囲を版番号だけで対応済みにしないこと

## Options

| 案 | 公開までの負担 | 実用導線の確認 | データ安全 | 判断 |
|---|---:|---:|---:|---|
| 従来の長時間試験とchecksum照合を維持 | 高い | できる | 維持 | 採用しない |
| 時間だけ短くし、checksumを任意で残す | 中程度 | できる | 維持 | 境界が曖昧 |
| 通常CIと短い実用確認へ絞る | 低い | できる | 内部検査を維持 | 採用 |

## Decision

プロジェクトオーナーの2026-08-23の明示判断により、リリース品質を通常CI、固定KonomiTVの既存実用証拠、
短いLinux lifecycle確認、公開文書の整合へ絞る。

時間を固定した耐久試験は、現在および次の安定版の品質目標、完了条件、必須test、必須証拠、必須成果物にしない。
特定時間の専用workflow、試験報告、resource推移表も要求しない。既存の短い回帰testと、通常運転で見つかった
不具合を修正する運用は維持する。

`SHA256SUMS`の生成、GitHub Releaseでの公開、利用者による照合、照合専用の製品機能は、次版の必須成果物と
完了条件から外す。既に公開したreleaseの資産と確認記録は履歴として変更しない。

次は維持する。

- 製品版、tag、公開対象commit、binaryへ埋め込んだVCS revisionの一致
- Linux amd64／arm64 archive、OCI image、license、README、CHANGELOG、必要な詳細文書
- DB migration manifest、backup manifest、SQLite integrity、Go module、toolchain取得などの内部整合性検査
- 通常test、shuffle、race、vet、module検証、既知脆弱性検査、主要build
- Fresh Ubuntuで一度完了する、導入、起動、更新、復元、切り戻し、通常削除、purgeの確認

v1.0.0の前にv0.9.0を置くことも必須にしない。固定KonomiTVの実用バックエンド境界、通常品質、短い導入確認、
公開文書、release identityが揃えば、リリース準備用の専用PRからv1.0.0へ進める。

## Consequences

### Positive

- 品質判定が利用者の実際の操作と通常CIへ集中する。
- 専用の長時間運転とchecksum照合機能を保守しなくてよい。
- READMEと導入手順が短くなり、初めての利用者が迷いにくい。
- v0.9.0を形式的に挟まず、完成状態からv1.0.0へ進める。

### Negative

- 長時間運転だけで現れる不具合を公開前に発見できる保証は弱くなる。
- 利用者が配布archiveを独自に照合したい場合、Sazanami専用の一覧fileは提供されない。

### Risks and mitigations

- 運転時間に依存する不具合: 通常の回帰test、短いlifecycle確認、実利用での問題報告を個別に修正する。
- 配布物の取り違え: tag、製品版、埋め込みVCS revision、workflowが作った同じarchiveの内容を確認する。
- 内部hashまで誤って削除する危険: 実用リリース品質仕様v1で、配布archiveと内部データの検査を明確に分ける。

## Verification

- 実用リリース品質仕様v1の`PRQ-001`～`PRQ-008`を製品CI、公開文書、Releaseへ対応付ける。
- Product READMEとLinux導入手順に、利用者必須のchecksum照合が残らないことを確認する。
- Release workflowが`SHA256SUMS`を生成または公開しないことを確認する。
- Activeな品質表、handoff、公開文書に、時間指定の耐久試験が必須項目として残らないことを確認する。
- DB migration、backup、SQLite integrity、Go module、toolchainの既存testが維持されることを確認する。
- 通常CIと短いLinux lifecycle確認を同じ最終candidateで成功させる。

## Product synchronization

- Handoff: Handoff 0062
- Planning source commit: 未確定
- Target product base commit: `0b90ee4d5cdd137c23cdb966649e436db0170ea8`
- Product destination: 同じpath
- Last synchronized product commit: None
- Known divergence: 製品branchにはHandoff 0061の旧authority copyと、再利用する公開文書commitがある

## Revisit when

- 実利用で、通常CIと短い確認では再現できない同種の重大障害が繰り返し発生したとき。
- 署名付きpackage repositoryなど、別の配布identityを正式に採用するとき。
- 固定KonomiTVまたは公開runtime境界を変更するとき。
