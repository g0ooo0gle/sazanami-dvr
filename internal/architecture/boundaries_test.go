package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPackageBoundaries(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test fileのpathを取得できません")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	fileSet := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if strings.HasPrefix(relative, "internal/core/") && forbiddenCoreImport(name) {
				t.Errorf("%s: coreから%sをimportできません", relative, name)
			}
			if strings.HasPrefix(relative, "internal/adapters/sqlite/") && strings.HasPrefix(name, "github.com/g0ooo0gle/sazanami-dvr/internal/app") {
				t.Errorf("%s: SQLite adapterからapplication packageをimportできません", relative)
			}
			if strings.HasPrefix(relative, "internal/adapters/webui/") && forbiddenWebUIImport(name) {
				t.Errorf("%s: WebUI adapterから%sをimportできません", relative, name)
			}
			if strings.HasPrefix(relative, "internal/app/opsui/") && forbiddenOpsUIImport(name) {
				t.Errorf("%s: OpsUI applicationから%sをimportできません", relative, name)
			}
			if strings.HasPrefix(relative, "internal/adapters/ctrlcmd/channel/") && forbiddenChannelImport(name) {
				t.Errorf("%s: CtrlCmd channel adapterから%sをimportできません", relative, name)
			}
			if !strings.HasPrefix(relative, "internal/adapters/ctrlcmd/channel/") &&
				!strings.HasPrefix(relative, "internal/adapters/ctrlcmd/runtime/") &&
				!strings.HasPrefix(relative, "internal/adapters/ctrlcmd/programguide/") &&
				name == "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel" {
				t.Errorf("%s: CtrlCmd channel型はruntime／番組表以外からimportできません", relative)
			}
			if strings.HasPrefix(relative, "internal/adapters/ctrlcmd/runtime/") && forbiddenCtrlCmdRuntimeImport(name) {
				t.Errorf("%s: CtrlCmd runtime compositionから%sをimportできません", relative, name)
			}
		}
		if strings.HasPrefix(relative, "internal/adapters/sqlite/") {
			assertNoExportedSQLHandle(t, relative, file)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func forbiddenCtrlCmdRuntimeImport(path string) bool {
	return path == "database/sql" || path == "net/http" || strings.Contains(path, "/adapters/provider") ||
		strings.Contains(path, "/adapters/sqlite") || strings.Contains(path, "/adapters/webui") ||
		strings.Contains(path, "/app/")
}

func forbiddenChannelImport(path string) bool {
	return path == "database/sql" || path == "net" || path == "net/http" || path == "os" || path == "path/filepath" ||
		strings.Contains(path, "/adapters/provider") || strings.Contains(path, "/adapters/sqlite") ||
		strings.Contains(path, "/adapters/webui") || strings.Contains(path, "/app/") || strings.Contains(path, "/core/")
}

func forbiddenWebUIImport(path string) bool {
	return path == "database/sql" || strings.Contains(path, "/adapters/sqlite") ||
		strings.Contains(path, "/adapters/ctrlcmd") || strings.Contains(path, "/core/provider")
}

func forbiddenOpsUIImport(path string) bool {
	return path == "database/sql" || path == "html/template" || path == "net/http" ||
		strings.Contains(path, "/adapters/") || strings.Contains(path, "ctrlcmd")
}

func forbiddenCoreImport(path string) bool {
	return path == "database/sql" || path == "encoding/json" || path == "net/http" ||
		strings.Contains(path, "sqlite") || strings.Contains(path, "ctrlcmd")
}

func assertNoExportedSQLHandle(t *testing.T, path string, file *ast.File) {
	t.Helper()
	sqlAliases := make(map[string]struct{})
	for _, imported := range file.Imports {
		name, err := strconv.Unquote(imported.Path.Value)
		if err != nil || name != "database/sql" {
			continue
		}
		alias := "sql"
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		sqlAliases[alias] = struct{}{}
	}
	if len(sqlAliases) == 0 {
		return
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || !ast.IsExported(function.Name.Name) {
			continue
		}
		ast.Inspect(function.Type, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			identifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, sqlAlias := sqlAliases[identifier.Name]; sqlAlias {
				t.Errorf("%s: exported function %sはdatabase/sql typeを公開できません", path, function.Name.Name)
				return false
			}
			return true
		})
	}
}
