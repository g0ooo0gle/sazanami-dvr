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
