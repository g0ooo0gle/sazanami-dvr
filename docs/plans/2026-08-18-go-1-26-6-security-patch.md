# Go 1.26.6 Security Patch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Local、CI、Release、Docker builderをGo 1.26.6へ固定し、Go 1.26.5の到達可能な既知脆弱性6件を解消する。

**Architecture:** 製品コードと依存graphは変更せず、toolchain authorityを一つのpatch版へそろえる。Architecture testで現在のpinを固定し、Hosted CIとDocker buildでも同じ値とdigestをread backする。

**Tech Stack:** Go 1.26.6、GitHub Actions、Docker Buildx、Alpine 3.23、govulncheck v1.6.0

## Global Constraints

- `go 1.26.0`、製品版0.3.0、schema 13、module path、依存module、`go.sum`を変更しない。
- `GOTOOLCHAIN=local`を維持し、暗黙downloadを許可しない。
- Docker builderは`golang:1.26.6-alpine3.23@sha256:e57c41c1d5864341031181b0db34b9a537bb5773eb6428e4e5bdaea0f9135406`へ固定する。
- 過去のAccepted文書、Handoff、公開済みRelease provenanceに残るGo 1.26.5を変更しない。
- 新しい依存、CGO、製品機能、API、DB、録画挙動を追加しない。

---

### Task 1: Accepted ADRを製品へ同期する

**Files:**
- Create: `docs/adr/0065-go-1-26-6-security-patch.md`

**Interfaces:**
- Consumes: Planning source `55eb18b98bcdfd6dfab67077feb4abe9426bce6f`
- Produces: SHA-256 `8dd02d4ce2766192cec2ba91bc9175437821387926f6d693aafcf12f1a1e7391`の製品側正本

- [x] **Step 1: Planning sourceからADR一件だけを複製する**

製品destinationとplanning sourceを`cmp`で照合する。

- [x] **Step 2: Hashとbyte数を確認する**

Run:

```bash
sha256sum docs/adr/0065-go-1-26-6-security-patch.md
wc -c docs/adr/0065-go-1-26-6-security-patch.md
```

Expected: `8dd02d4c...e7391`、`4936` bytes。

- [x] **Step 3: 文書同期だけをcommitする**

```bash
git add docs/adr/0065-go-1-26-6-security-patch.md
git commit -m 'docs: Go 1.26.6追従判断を同期する'
```

Completed commit: `c76b61cfa785cbdd95cf4635f34244f6fb1f3598`

### Task 2: Toolchain pinの不一致をtestで固定して更新する

**Files:**
- Create: `internal/architecture/toolchain_pins_test.go`
- Modify: `go.mod:5`
- Modify: `.github/workflows/ci.yml:29,36`
- Modify: `.github/workflows/release.yml:28,35`
- Modify: `Dockerfile:3`
- Modify: `README.md:183`
- Modify: `CONTRIBUTING.md:39`
- Modify: `docs/docker-compose.md`

**Interfaces:**
- Consumes: Go version `1.26.6`、Docker index digest `sha256:e57c41c...35406`
- Produces: `TestToolchainPins`によるrepository内のcurrent pin契約

- [ ] **Step 1: 失敗するarchitecture testを書く**

`internal/architecture/toolchain_pins_test.go`へ次を追加する。

```go
package architecture_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestToolchainPins(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test fileのpathを取得できません")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	expected := map[string][]string{
		"go.mod":                        {"toolchain go1.26.6"},
		".github/workflows/ci.yml":      {`go-version: "1.26.6"`, `= "go1.26.6"`},
		".github/workflows/release.yml": {`go-version: "1.26.6"`, `= "go1.26.6"`},
		"Dockerfile":                    {"golang:1.26.6-alpine3.23@sha256:e57c41c1d5864341031181b0db34b9a537bb5773eb6428e4e5bdaea0f9135406"},
		"README.md":                     {"Go 1.26.6"},
		"CONTRIBUTING.md":               {"Go 1.26.6"},
	}
	for name, values := range expected {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if !strings.Contains(string(content), value) {
				t.Errorf("%sに%sがありません", name, value)
			}
		}
	}
}
```

- [ ] **Step 2: Testが旧pinで失敗することを確認する**

Run:

```bash
go test -count=1 -run '^TestToolchainPins$' ./internal/architecture
```

Expected: Go 1.26.6の文字列がないためFAIL。

- [ ] **Step 3: 現在のpinだけを1.26.6へ更新する**

`go.mod`、CI、Release、Dockerfile、README、CONTRIBUTINGを上記固定値へ置き換える。
`docs/docker-compose.md`へADR-0065への参照と、builderがGo 1.26.6であることを一文追加する。

- [ ] **Step 4: Architecture testと既存targetを通す**

Run:

```bash
GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/architecture ./cmd/sazanami-dvr
```

Expected: PASS、`go env GOVERSION`は`go1.26.6`。

- [ ] **Step 5: Pin更新を一つのcommitにする**

```bash
git add go.mod .github/workflows/ci.yml .github/workflows/release.yml Dockerfile README.md CONTRIBUTING.md docs/docker-compose.md internal/architecture/toolchain_pins_test.go
git commit -m 'build: Go 1.26.6へ更新する'
```

### Task 3: 全build経路を検証してPRを更新する

**Files:**
- Modify only if verification reveals a defect in Task 2 files.

**Interfaces:**
- Consumes: Task 2のexact commit
- Produces: Product PR #58のGo 1.26.6 Hosted CI evidence

- [ ] **Step 1: Localの版と依存不変を確認する**

Run:

```bash
GOTOOLCHAIN=go1.26.6 go version
GOTOOLCHAIN=go1.26.6 go env GOVERSION
GOTOOLCHAIN=go1.26.6 go mod verify
git diff e10e2177fee3a8a999ec317a0e38656043445d59 -- go.sum
```

Expected: Go 1.26.6、module verified、`go.sum`差分なし。

- [ ] **Step 2: Full、shuffle、race、vet、脆弱性検査を直列実行する**

Run:

```bash
GOTOOLCHAIN=go1.26.6 go test -p 1 -count=1 ./...
GOTOOLCHAIN=go1.26.6 go test -p 1 -count=1 -shuffle=on ./...
GOTOOLCHAIN=go1.26.6 go test -p 1 -count=1 -race ./...
GOTOOLCHAIN=go1.26.6 go vet ./...
GOTOOLCHAIN=go1.26.6 go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
```

Expected: 全てexit 0、到達可能な既知脆弱性0件。

- [ ] **Step 3: CGO無効4環境buildを一時directoryへ出す**

Run each with `CGO_ENABLED=0` and `GOTOOLCHAIN=go1.26.6`:

```bash
build_dir=$(mktemp -d /private/tmp/sazanami-h0057-build.XXXXXX)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$build_dir/linux-amd64" ./cmd/sazanami-dvr
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$build_dir/linux-arm64" ./cmd/sazanami-dvr
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o "$build_dir/darwin-amd64" ./cmd/sazanami-dvr
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o "$build_dir/darwin-arm64" ./cmd/sazanami-dvr
```

Expected: 全build成功。`go version -m`でGo 1.26.6、CGO無効、対象OS／architecture、exact revisionを確認する。

- [ ] **Step 4: DockerとComposeを検証する**

Run:

```bash
docker buildx imagetools inspect golang:1.26.6-alpine3.23
docker compose --env-file packaging/compose/.env.example -f packaging/compose/compose.yaml config
docker compose --env-file packaging/compose/.env.example -f packaging/compose/compose.yaml -f packaging/compose/compose.konomitv-delete.yaml config
```

Expected: 固定index／amd64／arm64 manifest一致、Base read-only、override read-write、lock read-only。

- [ ] **Step 5: Diffと不変条件を確認する**

Run:

```bash
git diff --check
git diff e10e2177fee3a8a999ec317a0e38656043445d59 -- go.sum
git grep -n '1\.26\.5'
git status --short
```

Expected: 空白不良なし、`go.sum`差分なし。1.26.5は過去のAccepted文書、Handoff、release provenanceだけに残る。Working tree clean。

- [ ] **Step 6: Product branchをpushしHosted CIをread backする**

```bash
git push origin codex/h0056-konomitv-recording-delete
```

Expected: Product PR #58のheadとremote headが一致し、Go品質確認とContainer構成確認が同じheadでSUCCESS。
