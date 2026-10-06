package otelkit

import (
	"cmp"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	pkgs := map[string][]string{}
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
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			pkgs[filepath.Dir(path)] = append(pkgs[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range slices.Sorted(maps.Keys(pkgs)) {
		for _, f := range packageLevelInstruments(t, root, pkgs[dir]) {
			t.Errorf("%s: инструмент OTel на уровне пакета — создавайте его в конструкторе", f)
		}
	}
}

// Страж видит и функцию своего пакета, которая создаёт инструмент сама или через
// другую функцию пакета; замыкание, которое выполнится позже (конструктор fx), — нет.
func TestPackageLevelInstruments_SeesPackageHelpers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kit.go")
	src := `package kit

import "` + ModulePath + `/internal/otelkit"

var direct = otelkit.Meter("kit")

var viaHelper = newCounter()

var deferred = provide(func() any { return newCounter() })

func provide(constructor func() any) any { return constructor }

func newCounter() any { return meter() }

func meter() any { return otelkit.Meter("kit") }
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{"kit.go:5 otelkit.Meter", "kit.go:7 newCounter"}
	if got := packageLevelInstruments(t, dir, []string{path}); !slices.Equal(got, want) {
		t.Fatalf("found %v, want %v", got, want)
	}
}

// packageLevelInstruments — инициализаторы package-level var файлов пакета paths,
// которые зовут Meter/Tracer/Get*Provider пакетов otelkit и otel напрямую (и в
// замыкании: sync.OnceValue держит инструмент вечно) или через функцию своего пакета.
func packageLevelInstruments(t *testing.T, root string, paths []string) []string {
	t.Helper()
	fset := token.NewFileSet()
	files := make([]*ast.File, len(paths))
	seams := make([]map[string]bool, len(paths))
	for i, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[i], seams[i] = file, seamNames(t, path, file)
	}
	// Функция создаёт инструмент, если зовёт шов или другую такую функцию пакета: до неподвижной точки.
	makers := map[string]bool{}
	for grown := true; grown; {
		grown = false
		for i, file := range files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Recv == nil && fn.Body != nil && !makers[fn.Name.Name] &&
					len(seamCalls(fn.Body, seams[i]))+len(helperCalls(fn.Body, makers)) > 0 {
					makers[fn.Name.Name], grown = true, true
				}
			}
		}
	}
	var out []string
	for i, file := range files {
		rel, err := filepath.Rel(root, paths[i])
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			calls := append(seamCalls(gen, seams[i]), helperCalls(gen, makers)...)
			slices.SortFunc(calls, func(a, b *ast.CallExpr) int { return cmp.Compare(a.Pos(), b.Pos()) })
			for _, call := range calls {
				out = append(out, rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+" "+callee(call))
			}
		}
	}
	return out
}

// seamNames — имена, под которыми файл импортирует otelkit и otel.
func seamNames(t *testing.T, path string, file *ast.File) map[string]bool {
	t.Helper()
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
	return seams
}

// seamCalls — вызовы Meter/Tracer/Get*Provider шва (otelkit, otel) в n, в том числе в
// замыканиях.
func seamCalls(n ast.Node, seams map[string]bool) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(n, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && seams[pkg.Name] {
					switch sel.Sel.Name {
					case "Meter", "Tracer", "GetMeterProvider", "GetTracerProvider":
						out = append(out, call)
					}
				}
			}
		}
		return true
	})
	return out
}

// helperCalls — вызовы функций пакета из makers в n вне замыканий: тело замыкания
// выполнится позже (конструктор fx), а не при инициализации пакета.
func helperCalls(n ast.Node, makers map[string]bool) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(n, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if fn, ok := call.Fun.(*ast.Ident); ok && makers[fn.Name] {
				out = append(out, call)
			}
		}
		return true
	})
	return out
}

// callee — вызванная функция для сообщения: «pkg.Name» или «name».
func callee(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if pkg, ok := fn.X.(*ast.Ident); ok {
			return pkg.Name + "." + fn.Sel.Name
		}
	}
	return "?"
}
