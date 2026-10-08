package testkit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Образ с плавающим тегом меняет registry, и тест перестаёт быть воспроизводимым. Ловит строковый
// литерал Go-кода модуля, включая тесты, который кончается таким тегом.
func TestImageTagsArePinned(t *testing.T) {
	floating := ":" + "latest"
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && strings.HasSuffix(s, floating) {
				t.Errorf("%s: образ %q — с плавающим тегом, нужен конкретный", fset.Position(lit.Pos()), s)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
