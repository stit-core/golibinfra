package logkit_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Ключ ошибки в логах китов — `error` (его же пишет FxLogger): запросы и алерты по одному ключу
// видят все киты. Ловит литерал "err" аргументом вызова (Warn, slog.String, append) или элементом
// составного литерала ([]any{…}) в коде модуля вне тестов.
func TestLogErrorKeyIsError(t *testing.T) {
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
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var elems []ast.Expr
			switch n := n.(type) {
			case *ast.CallExpr:
				elems = n.Args
			case *ast.CompositeLit:
				elems = n.Elts
			}
			for _, e := range elems {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == `"err"` {
					t.Errorf("%s: ключ ошибки лога — \"error\", не \"err\"", fset.Position(lit.Pos()))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
