# ADR-0065: Goのセキュリティ修正patchを全build経路で固定する

- Status: Accepted
- Proposed date: 2026-08-18
- Decision date: 2026-08-18
- Owners: Project owner
- Decision reviewers: Codex
- Related requirements: COPY-GO-TOOLCHAIN、CI-D、release verification
- Related planning documents: Plan 0081
- Related handoffs: Handoff 0056、Handoff 0057
- Product copy path: `docs/adr/0065-go-1-26-6-security-patch.md`
- Product sync state: NOT COPIED
- Supersedes: None
- Superseded by: None

## Context

製品は初期基準としてGo 1.26.5を`go.mod`、CI、Release、Docker builderへ固定している。2026-08-18、
製品Draft PR #58の`govulncheck v1.6.0`は、この標準ライブラリに到達可能な既知脆弱性6件を検出した。
Go公式配布JSONはGo 1.26.6をstableとして公開し、各advisoryも同版を修正版として示す。
確認元は<https://go.dev/dl/?mode=json>と`https://pkg.go.dev/vuln/<GO-ID>`である。Docker builderは
`docker buildx imagetools inspect`でDocker Official Imageのindexと対象architectureを読み戻した。

これは製品機能の不具合ではないが、既知脆弱性ゼロを必須にする既存CIとrelease gateを満たさない。CIだけを
新しくしても、Release binaryまたはDocker imageが古いtoolchainで残る。再現性を維持したまま、全build経路を
同じ修正済みpatch版へ更新する判断が必要である。

## Decision drivers

- 到達可能な既知脆弱性を配布物へ残さない。
- Local、CI、Release、Dockerで同じtoolchainを使う。
- 暗黙downloadとmoving tagを避け、完全な版とdigestで再現できる。
- 言語版、依存、製品版、schema、API、機能を変えない。
- 過去の初期toolchainとrelease provenanceを履歴として維持する。

## Options

### Option A: Go 1.26.6を全build経路へ固定する

`go.mod`、CI、ReleaseをGo 1.26.6へそろえ、Docker builderを
`golang:1.26.6-alpine3.23@sha256:e57c41c1d5864341031181b0db34b9a537bb5773eb6428e4e5bdaea0f9135406`
へ固定する。現在版の利用手順だけを更新し、過去の証拠は書き換えない。

### Option B: CIだけGo 1.26.6へ更新する

検査は通るが、ReleaseとDocker imageに1.26.5が残る。配布経路の安全性とprovenanceが一致しない。

### Option C: 最新patchへ自動追従する

修正適用は早いが、同じcommitでもtoolchainとimageが時刻で変わる。既存のexact readbackと再現性を失う。

## Decision

Project ownerの2026-08-18の明示レビューによりOption Aを採用する。Go 1.26.6を`go.mod`、CI、Release、Docker builderへ固定し、
`GOTOOLCHAIN=local`とDocker multi-platform index digestを維持する。

Handoff 0057がReadyになり、製品側copyがread backされるまで、製品pinを変更しない。

## Consequences

### Positive

- 既知の標準ライブラリ脆弱性を同じpatch版で解消できる。
- CIで検査したtoolchainとRelease／Docker配布物が一致する。
- 言語版、依存、製品機能を変えずに更新できる。

### Negative

- Local、Hosted、Dockerの全経路でtestとprovenance確認をやり直す必要がある。
- Docker builder digestと現在版文書の更新が必要になる。

### Risks and mitigations

- Patch版の回帰は、通常、shuffle、race、vet、主要四環境build、Compose image buildで検出する。
- Digestの取り違えは、indexと対象architecture manifestを別々にread backして防ぐ。
- 一部経路の更新漏れは、repository全体の`1.26.5`検索とbinary build info検査で防ぐ。過去の証拠文書は
  検索結果から区別し、書き換えない。

## Verification

- Go公式配布JSONでGo 1.26.6がstableであり、対象archive SHA-256が一致する。
- Docker Official Imageのindex digest、amd64／arm64 manifest、source revisionが一致する。
- `go version`、`go env GOVERSION`、CI、Release、Docker binaryがGo 1.26.6を示す。
- `govulncheck v1.6.0`が到達可能な既知脆弱性を報告しない。
- Full、shuffle、race、vet、module検証、CGO無効4環境build、Compose image buildが成功する。
- 製品版、schema 13、依存graph、API、機能差分がない。

## Product synchronization

- Handoff: Handoff 0057（Draft予定）
- Planning source commit: NOT FIXED
- Target product base commit: NOT FIXED
- Product destination: `docs/adr/0065-go-1-26-6-security-patch.md`
- Last synchronized product commit: NOT COPIED
- Known divergence: 製品と既存Accepted toolchain仕様はGo 1.26.5のまま

## Revisit when

- Go 1.26.6に製品へ影響する回帰が見つかる。
- Go 1.26系の後続security patchが必要になる。
- Go 1.27以降へ言語版またはtoolchain世代を更新する。
- Docker Official Imageの固定digestが利用不能または撤回される。
