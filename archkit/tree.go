package archkit

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	domainDir   = "internal/domain"
	platformDir = "internal/platform"
	pkgDir      = "pkg"
)

// goFile — Go-файл модуля: путь и каталог пакета от корня модуля (через "/"), домен и подпакет
// в нём (вне доменов — пустые), импорты и разбор.
type goFile struct {
	path    string
	pkg     string
	domain  string
	sub     string
	test    bool
	imports []string
	syntax  *ast.File
}

// domainCode — не-тестовый файл домена: только такие разобраны целиком, остальные — до импортов.
func (f goFile) domainCode() bool { return f.domain != "" && !f.test }

// violation — нарушение правила: what — что и где (ключ известного нарушения), why — правило.
type violation struct{ what, why string }

// tree — модуль потребителя, каким его видят правила.
type tree struct {
	module     string
	opts       options
	files      []goFile
	pkgImports map[string][]string // пакет → импорты его не-тестовых файлов
	tables     []string            // таблицы приложения: CREATE TABLE в migrations/*.up.sql
	queries    map[string]string   // домен → SQL его infra/postgres/queries
	ioChains   map[string][]string
	seen       map[string]bool
	found      []violation
}

var moduleLine = regexp.MustCompile(`(?m)^module[ \t]+"?([^\s"]+)`)

// load читает модуль из fsys: module path — из go.mod, Go-файлы — те, что видит go build (без
// каталогов на "." и "_", testdata, vendor и вложенных модулей), SQL — миграции и запросы доменов.
func load(fsys fs.FS, o options) (*tree, error) {
	gomod, err := fs.ReadFile(fsys, "go.mod")
	if err != nil {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}
	m := moduleLine.FindSubmatch(gomod)
	if m == nil {
		return nil, errors.New("go.mod has no module line")
	}
	t := &tree{
		module:     string(m[1]),
		opts:       o,
		pkgImports: map[string][]string{},
		queries:    map[string]string{},
		ioChains:   map[string][]string{},
		seen:       map[string]bool{},
	}
	fset := token.NewFileSet()
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if p != "." && (hiddenDir(d.Name()) || nestedModule(fsys, p)) {
				return fs.SkipDir
			}
			return nil
		case !d.Type().IsRegular() || !strings.HasSuffix(p, ".go"):
			return nil
		}
		f, err := parseFile(fsys, fset, p)
		if err != nil {
			return err
		}
		t.files = append(t.files, f)
		if !f.test {
			t.pkgImports[f.pkg] = append(t.pkgImports[f.pkg], f.imports...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	domains := t.domains()
	if len(domains) == 0 {
		return nil, errors.New("no Go packages under " + domainDir + ": the rules hold the layout " + domainDir + "/<d>")
	}
	if err := t.loadSQL(fsys, domains); err != nil {
		return nil, err
	}
	return t, nil
}

func hiddenDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor"
}

func nestedModule(fsys fs.FS, dir string) bool {
	_, err := fs.Stat(fsys, path.Join(dir, "go.mod"))
	return err == nil
}

func parseFile(fsys fs.FS, fset *token.FileSet, p string) (goFile, error) {
	f := goFile{path: p, pkg: path.Dir(p), test: strings.HasSuffix(p, "_test.go")}
	f.domain, f.sub, _ = splitDomain(f.pkg)
	src, err := fs.ReadFile(fsys, p)
	if err != nil {
		return f, err
	}
	mode := parser.ImportsOnly | parser.SkipObjectResolution
	if f.domainCode() {
		mode = parser.SkipObjectResolution
	}
	if f.syntax, err = parser.ParseFile(fset, p, src, mode); err != nil {
		return f, err
	}
	for _, spec := range f.syntax.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return f, fmt.Errorf("%s: import %s: %w", f.path, spec.Path.Value, err)
		}
		f.imports = append(f.imports, imp)
	}
	return f, nil
}

// domains — домены модуля: каталоги internal/domain с Go-кодом, по алфавиту.
func (t *tree) domains() []string {
	var names []string
	for _, f := range t.files {
		if f.domain != "" && !slices.Contains(names, f.domain) {
			names = append(names, f.domain)
		}
	}
	slices.Sort(names)
	return names
}

func (t *tree) add(what, why string) {
	if t.seen[what] {
		return
	}
	t.seen[what] = true
	t.found = append(t.found, violation{what: what, why: why})
}

// inModule — путь пакета модуля от его корня: example.org/shop/pkg/policy → "pkg/policy", true;
// пакет в самом корне — ".".
func (t *tree) inModule(imp string) (string, bool) {
	if imp == t.module {
		return ".", true
	}
	return strings.CutPrefix(imp, t.module+"/")
}

// name — импорт, каким его называет нарушение: пакет модуля — путём от корня.
func (t *tree) name(imp string) string {
	rel, _ := t.inModule(imp)
	return rel
}

// splitDomain разбирает путь пакета от корня модуля на домен и подпакет:
// internal/domain/user/infra/postgres → "user", "infra/postgres", true; вне доменов — "", "", false.
func splitDomain(pkg string) (domain, sub string, ok bool) {
	rest, ok := strings.CutPrefix(pkg, domainDir+"/")
	if !ok {
		return "", "", false
	}
	domain, sub, _ = strings.Cut(rest, "/")
	return domain, sub, true
}

// under — путь p совпадает с dir или лежит внутри него.
func under(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// importsByName — пакеты файла по имени, под которым файл их видит: алиас или последний элемент
// пути.
func (f goFile) importsByName() map[string]string {
	byName := map[string]string{}
	for i, spec := range f.syntax.Imports {
		name := path.Base(f.imports[i])
		if spec.Name != nil {
			name = spec.Name.Name
		}
		byName[name] = f.imports[i]
	}
	return byName
}
