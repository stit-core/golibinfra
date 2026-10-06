package otelkit

import (
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

// Страж видит инструмент в коде инициализации пакета — инициализаторе var, init() и
// sync.OnceValue/OnceValues/OnceFunc — напрямую и через функцию, метод и значение функции
// пакета; замыкание, которое выполнит позже конструктор fx, — нет.
func TestPackageLevelInstruments_SeesPackageHelpers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kit.go")
	src := `package kit

import (
	"sync"

	"` + ModulePath + `/internal/otelkit"
)

var direct = otelkit.Meter("kit")

var viaHelper = newCounter()

var deferred = provide(func() any { return newCounter() })

var lazy = sync.OnceValue(func() any { return newCounter() })

var lazyRef = sync.OnceValue(newCounter)

var viaMethod = factory{}.counter()

var mk = newCounter

var viaValue = mk()

var viaInit any

func init() { viaInit = newCounter() }

type factory struct{}

func (factory) counter() any { return meter() }

func provide(constructor func() any) any { return constructor }

func newCounter() any { return meter() }

func meter() any { return otelkit.Meter("kit") }
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"kit.go:9 otelkit.Meter", "kit.go:11 newCounter", "kit.go:15 newCounter", "kit.go:17 sync.OnceValue",
		"kit.go:19 counter", "kit.go:23 mk", "kit.go:27 newCounter",
	}
	if got := packageLevelInstruments(t, dir, []string{path}); !slices.Equal(got, want) {
		t.Fatalf("found %v, want %v", got, want)
	}
}

// packageLevelInstruments — вызовы в коде инициализации пакета paths (инициализаторы var,
// init()), которые создают инструмент: шов (Meter/Tracer/Get*Provider пакетов otelkit и otel)
// или функция, метод, значение функции своего пакета, которые зовут шов.
func packageLevelInstruments(t *testing.T, root string, paths []string) []string {
	t.Helper()
	fset := token.NewFileSet()
	files := make([]*ast.File, len(paths))
	scopes := make([]fileScope, len(paths))
	for i, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[i], scopes[i] = file, newFileScope(t, path, file)
	}
	m := makers{funcs: map[string]bool{}, methods: map[string]bool{}}
	for grown := true; grown; { // до неподвижной точки: функция пакета зовёт другую такую же
		grown = false
		for i, file := range files {
			for _, decl := range file.Decls {
				grown = m.learn(decl, scopes[i]) || grown
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
			var calls []*ast.CallExpr
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok == token.VAR {
					calls = m.creating(d, scopes[i])
				}
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == "init" && d.Body != nil {
					calls = m.creating(d.Body, scopes[i])
				}
			}
			for _, call := range calls {
				out = append(out, rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+" "+callee(call))
			}
		}
	}
	return out
}

// makers — функции, методы (по имени) и пакетные значения функций (var mk = newCounter) своего
// пакета, которые создают инструмент.
type makers struct{ funcs, methods map[string]bool }

// learn добавляет в makers функцию, метод или значение функции из decl; true — добавил.
func (m makers) learn(decl ast.Decl, fs fileScope) bool {
	grown := false
	add := func(set map[string]bool, name string) {
		if !set[name] {
			set[name], grown = true, true
		}
	}
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Body == nil || len(m.creating(d.Body, fs)) == 0 {
			break
		}
		if d.Recv == nil {
			add(m.funcs, d.Name.Name)
		} else {
			add(m.methods, d.Name.Name)
		}
	case *ast.GenDecl:
		if d.Tok != token.VAR {
			break
		}
		for _, spec := range d.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for j, v := range vs.Values {
				if j < len(vs.Names) && m.isMaker(v, fs) {
					add(m.funcs, vs.Names[j].Name)
				}
			}
		}
	}
	return grown
}

// isMaker — e — функция, создающая инструмент: функция, метод или значение функции пакета из
// makers либо литерал функции, который зовёт шов или их.
func (m makers) isMaker(e ast.Expr, fs fileScope) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return m.funcs[e.Name]
	case *ast.SelectorExpr:
		return !fs.isImport(e.X) && m.methods[e.Sel.Name]
	case *ast.FuncLit:
		return len(m.creating(e.Body, fs)) > 0
	}
	return false
}

// creating — вызовы в n, которые создают инструмент: шов или функция из makers. Замыкание
// выполнится позже (конструктор fx) и пропускается, кроме аргумента sync.OnceValue,
// OnceValues и OnceFunc: его результат живёт в пакете.
func (m makers) creating(n ast.Node, fs fileScope) []*ast.CallExpr {
	var out []*ast.CallExpr
	eager := map[*ast.FuncLit]bool{}
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return eager[n]
		case *ast.CallExpr:
			made := fs.isSeam(n.Fun) || m.isMaker(n.Fun, fs)
			if fs.isOnce(n.Fun) {
				for _, arg := range n.Args {
					if lit, ok := arg.(*ast.FuncLit); ok {
						eager[lit] = true
					} else if m.isMaker(arg, fs) {
						made = true
					}
				}
			}
			if made {
				out = append(out, n)
			}
		}
		return true
	})
	return out
}

// fileScope — импорты файла: имя в файле → путь пакета.
type fileScope map[string]string

func newFileScope(t *testing.T, path string, file *ast.File) fileScope {
	t.Helper()
	fs := fileScope{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("%s: import %s: %v", path, imp.Path.Value, err)
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		fs[name] = p
	}
	return fs
}

func (fs fileScope) isImport(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && fs[id.Name] != ""
}

// pkgFunc — путь пакета и имя функции вызова pkg.Name; пусто — не вызов функции пакета.
func (fs fileScope) pkgFunc(fun ast.Expr) (path, name string) {
	if sel, ok := fun.(*ast.SelectorExpr); ok && fs.isImport(sel.X) {
		return fs[sel.X.(*ast.Ident).Name], sel.Sel.Name
	}
	return "", ""
}

// isSeam — Meter/Tracer/Get*Provider шва: otelkit и otel.
func (fs fileScope) isSeam(fun ast.Expr) bool {
	path, name := fs.pkgFunc(fun)
	if path != ModulePath+"/internal/otelkit" && path != "go.opentelemetry.io/otel" {
		return false
	}
	return name == "Meter" || name == "Tracer" || name == "GetMeterProvider" || name == "GetTracerProvider"
}

func (fs fileScope) isOnce(fun ast.Expr) bool {
	path, name := fs.pkgFunc(fun)
	return path == "sync" && (name == "OnceValue" || name == "OnceValues" || name == "OnceFunc")
}

// callee — вызванное для сообщения: «pkg.Name», «x.method», «name» или «method».
func callee(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
		return fn.Sel.Name
	}
	return "?"
}
