package architecture_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPracticalReleaseQualitySurface(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test fileのpathを取得できません")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))

	read := func(name string) string {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	requireText := func(name, content string, expected ...string) {
		t.Helper()
		for _, value := range expected {
			if !strings.Contains(content, value) {
				t.Errorf("%sに%sがありません", name, value)
			}
		}
	}
	rejectText := func(name, content string, rejected ...string) {
		t.Helper()
		for _, value := range rejected {
			if strings.Contains(content, value) {
				t.Errorf("%sに不要な%sが残っています", name, value)
			}
		}
	}

	publicDocs := []string{"README.md", "CHANGELOG.md", "CONTRIBUTING.md"}
	docPaths, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range docPaths {
		publicDocs = append(publicDocs, filepath.Join("docs", filepath.Base(path)))
	}
	for _, name := range publicDocs {
		content := read(name)
		rejectText(name, content,
			"72時間", "時間指定の耐久試験", "長時間試験",
			"SHA256SUMS", "sha256sum --ignore-missing")
	}

	release := read(".github/workflows/release.yml")
	rejectText(".github/workflows/release.yml", release, "SHA256SUMS")
	requireText(".github/workflows/release.yml", release,
		"CHANGELOG.md", "dist/OCI_IMAGE", "--platform linux/amd64,linux/arm64")

	ci := read(".github/workflows/ci.yml")
	requireText(".github/workflows/ci.yml", ci,
		"Linux lifecycle確認", "packaging/lifecycle/verify.sh")

	lifecycle := read("packaging/lifecycle/verify.sh")
	requireText("packaging/lifecycle/verify.sh", lifecycle,
		"/opt/sazanami-dvr", "/etc/sazanami-dvr", "/var/lib/sazanami-dvr",
		"systemctl", "db backup", "db restore")
	rejectText("packaging/lifecycle/verify.sh", lifecycle,
		"72時間", "SHA256SUMS", "sha256sum")

	komorebi := read("spec/recording/komorebi-original-live-hls-v1.md")
	requireText("spec/recording/komorebi-original-live-hls-v1.md", komorebi,
		"Active override: ADR-0069", "長時間確認（履歴）")
	rejectText("spec/recording/komorebi-original-live-hls-v1.md", komorebi,
		"72時間試験へ組み込む")
	if _, err := os.Stat(filepath.Join(root, "internal/app/catalogsync/target_soak_test.go")); !os.IsNotExist(err) {
		t.Errorf("専用の長時間soak試験が残っています: %v", err)
	}
	for _, name := range []string{
		"spec/operations/linux-installation-lifecycle-v1.md",
		"spec/operations/linux-installation-lifecycle-v2.md",
		"spec/operations/linux-lifecycle-verification-v1.md",
	} {
		requireText(name, read(name), "- Status: Superseded", "Superseded by:")
	}
}
