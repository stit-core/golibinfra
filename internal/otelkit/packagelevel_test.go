package otelkit

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

// Делегат OTel привязывает пакетный инструмент к первому SetMeterProvider
// процесса: второй провайдер (тест, второе приложение) его не видит.
func TestInstrumentsAreNotPackageLevel(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
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
		found = append(found, packageLevelInstruments(t, root, path)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Errorf("%s: инструмент OTel на уровне пакета — создавайте его в конструкторе", f)
	}
}

// packageLevelInstruments — вызовы Meter/Tracer/Get*Provider пакетов otelkit и
// otel в инициализаторах package-level var файла path.
func packageLevelInstruments(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	seams := map[string]bool{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("%s: import %s: %v", path, imp.Path.Value, err)
		}
		if p != ModulePath+"/internal/otelkit" && p != "go.opentelemetry.io/otel" {
			continue
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		seams[name] = true
	}
	if len(seams) == 0 {
		return nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		ast.Inspect(gen, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || !seams[pkg.Name] {
				return true
			}
			switch sel.Sel.Name {
			case "Meter", "Tracer", "GetMeterProvider", "GetTracerProvider":
				out = append(out, rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+" "+pkg.Name+"."+sel.Sel.Name)
			}
			return true
		})
	}
	return out
}
