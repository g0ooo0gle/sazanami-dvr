package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExportedTypesAndFunctionsHaveJapaneseDocsは、保守時の入口になる宣言から説明が失われるのを防ぐ。
func TestExportedTypesAndFunctionsHaveJapaneseDocs(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test fileのpathを取得できません")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	fileSet := token.NewFileSet()
	packageDirectories := make(map[string]string)
	documentedPackages := make(map[string]bool)

	err := filepath.WalkDir(repositoryRoot, func(path string, entry os.DirEntry, walkErr error) error {
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

		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		if file.Name.Name != "main" {
			directory := filepath.Dir(path)
			packageDirectories[directory] = file.Name.Name
			if file.Doc != nil && strings.HasPrefix(file.Doc.Text(), "Package "+file.Name.Name) && hasJapanese(file.Doc.Text()) {
				documentedPackages[directory] = true
			}
		}
		for _, declaration := range file.Decls {
			switch value := declaration.(type) {
			case *ast.FuncDecl:
				if ast.IsExported(value.Name.Name) {
					requireDoc(t, fileSet, repositoryRoot, value.Name.Name, value.Doc, value.Pos())
				}
			case *ast.GenDecl:
				if value.Tok != token.TYPE {
					continue
				}
				for _, specification := range value.Specs {
					typeSpec := specification.(*ast.TypeSpec)
					if !ast.IsExported(typeSpec.Name.Name) {
						continue
					}
					doc := typeSpec.Doc
					if doc == nil && len(value.Specs) == 1 {
						doc = value.Doc
					}
					requireDoc(t, fileSet, repositoryRoot, typeSpec.Name.Name, doc, typeSpec.Pos())
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for directory, packageName := range packageDirectories {
		if documentedPackages[directory] {
			continue
		}
		relative, relativeErr := filepath.Rel(repositoryRoot, directory)
		if relativeErr != nil {
			relative = directory
		}
		t.Errorf("%s: Package %sから始まる日本語のpackage説明が必要です", relative, packageName)
	}
}

// requireDocはGoDoc形式と、日本語を少なくとも1文字含む説明を要求する。
func requireDoc(t *testing.T, fileSet *token.FileSet, root, name string, doc *ast.CommentGroup, position token.Pos) {
	t.Helper()
	location := fileSet.Position(position)
	relative, err := filepath.Rel(root, location.Filename)
	if err != nil {
		relative = location.Filename
	}
	if doc == nil || !strings.HasPrefix(doc.Text(), name) {
		t.Errorf("%s:%d: %sから始まるGoDoc形式の説明が必要です", relative, location.Line, name)
		return
	}
	if !hasJapanese(doc.Text()) {
		t.Errorf("%s:%d: %sの説明には自然な日本語を含めてください", relative, location.Line, name)
	}
}

// hasJapaneseは説明に日本語の文章が含まれるかを最小限に確認する。
func hasJapanese(value string) bool {
	return strings.ContainsAny(value, "あいうえおかきくけこさしすせそたちつてとなにぬねのはひふへほまみむめもやゆよらりるれろわをん")
}
