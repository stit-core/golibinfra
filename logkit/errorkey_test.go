package logkit_test

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

// Ключ ошибки в логах китов — `error` (его же пишет FxLogger): запросы и алерты по одному ключу
// видят все киты. Ловит литерал "err" (и сырой) аргументом вызова (Warn, slog.String, append),
// элементом составного литерала ([]any{…}) или значением его поля (slog.Attr{Key: …}) в коде
// модуля вне тестов.
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
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					e = kv.Value
				}
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if key, err := strconv.Unquote(lit.Value); err == nil && key == "err" {
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
