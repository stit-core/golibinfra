// Command envdoc (make envdoc-check) сверяет env-переменные кода с доками китов:
// строка таблицы и дефолт у каждой переменной, нет переменных-призраков в доках,
// нет полей конфига без тега, которые ридер ENV отверг бы. Правила — check.
// С флагом -vars печатает переменные кода с дефолтами — вход make release-diff.
package main

import (
	_ "embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// allowList — UPPER_SNAKE-токены доков, которые не переменные китов: стандартные
// переменные зависимостей (OTel SDK, grpc-go, testcontainers) и константы протоколов.
//
//go:embed allow.txt
var allowList string

const rootTableHeading = "## Per-concern endpoints"

var (
	endpointRe = regexp.MustCompile(`_(URL|DSN|ENDPOINT|ADDR|TARGET)$`)
	tokenRe    = regexp.MustCompile("`([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)`")
	defaultRe  = regexp.MustCompile(`(?i)default|дефолт|по умолчанию`)
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-vars" {
		vars, err := codeVars(os.DirFS("."))
		if err != nil {
			fmt.Fprintln(os.Stderr, "envdoc:", err)
			os.Exit(2)
		}
		for _, v := range vars {
			fmt.Println(v)
		}
		return
	}
	problems, checked, err := check(os.DirFS("."))
	if err != nil {
		fmt.Fprintln(os.Stderr, "envdoc:", err)
		os.Exit(2)
	}
	for _, p := range problems {
		fmt.Println("envdoc:", p)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
	fmt.Printf("OK: envdoc — %d переменных кода сходятся с доками\n", checked)
}

// envVar — переменная окружения из кода: envconfig-тег или литерал os.Getenv/LookupEnv.
type envVar struct {
	name string
	def  string // default-тег; "" — дефолта нет
	zero string // нулевое значение типа поля, как его пишут доки (false, 0)
	pos  string // файл:строка
	dir  string // каталог пакета
}

// check — все правила разом:
//   - переменная — строка таблицы в README.md или docs/*.md своего каталога или
//     каталога выше; ячейка дефолта таблицы совпадает с default-тегом (без тега —
//     «—» или нулевое значение типа); каталог без таких доков — ошибка;
//   - поле без envconfig-тега в структуре с тегами — ошибка, если оно экспортируется,
//     не помечено ignored:"true" и не вложенный конфиг: ридер ENV его отвергает;
//   - UPPER_SNAKE-токен доков китов — переменная кода или строка allow.txt;
//   - корневая таблица § Per-concern endpoints — переменные кода с теми же дефолтами,
//     и в ней каждая endpoint-переменная (_URL, _DSN, _ENDPOINT, _ADDR, _TARGET).
func check(fsys fs.FS) (problems []string, checked int, err error) {
	pkgs, err := parsePackages(fsys)
	if err != nil {
		return nil, 0, err
	}
	allow := allowed()
	var vars []envVar
	for _, dir := range slices.Sorted(maps.Keys(pkgs)) {
		v, p := pkgs[dir].envVars(pkgs, allow)
		vars = append(vars, v...)
		problems = append(problems, p...)
	}
	docs, err := parseDocs(fsys)
	if err != nil {
		return nil, 0, err
	}
	problems = append(problems, checkVars(vars, docs)...)
	problems = append(problems, checkTokens(vars, docs, allow)...)
	problems = append(problems, checkRootTable(vars, docs, allow)...)
	return problems, len(vars), nil
}

func allowed() map[string]bool {
	allow := map[string]bool{}
	for line := range strings.Lines(allowList) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			allow[line] = true
		}
	}
	return allow
}

// codeVars — переменные кода дерева строками «имя=default», отсортированные и без повторов.
// Проблемы дерева не ошибка: make release-diff читает так и прошлую версию.
func codeVars(fsys fs.FS) ([]string, error) {
	pkgs, err := parsePackages(fsys)
	if err != nil {
		return nil, err
	}
	allow := allowed()
	set := map[string]bool{}
	for _, p := range pkgs {
		vars, _ := p.envVars(pkgs, allow)
		for _, v := range vars {
			set[v.name+"="+v.def] = true
		}
	}
	return slices.Sorted(maps.Keys(set)), nil
}

// pkg — не-тестовые Go-файлы одного каталога.
type pkg struct {
	dir     string
	module  string
	fset    *token.FileSet
	files   []*ast.File
	structs map[string]*ast.StructType
}

func parsePackages(fsys fs.FS) (map[string]*pkg, error) {
	module := ""
	if gomod, err := fs.ReadFile(fsys, "go.mod"); err == nil {
		for line := range strings.Lines(string(gomod)) {
			if rest, ok := strings.CutPrefix(line, "module "); ok {
				module = strings.TrimSpace(rest)
			}
		}
	}
	fset := token.NewFileSet()
	pkgs := map[string]*pkg{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		dir := path.Dir(p)
		if pkgs[dir] == nil {
			pkgs[dir] = &pkg{dir: dir, module: module, fset: fset, structs: map[string]*ast.StructType{}}
		}
		pk := pkgs[dir]
		pk.files = append(pk.files, f)
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok {
				for _, spec := range gd.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						if st, ok := ts.Type.(*ast.StructType); ok {
							pk.structs[ts.Name.Name] = st
						}
					}
				}
			}
		}
		return nil
	})
	return pkgs, err
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor"
}

func (p *pkg) envVars(pkgs map[string]*pkg, allow map[string]bool) (vars []envVar, problems []string) {
	for _, f := range p.files {
		osName := importName(f, "os")
		named := map[*ast.StructType]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.TypeSpec:
				if st, ok := n.Type.(*ast.StructType); ok {
					named[st] = true
					if hasEnvTag(st) {
						v, pr := p.structVars(pkgs, f, n.Name.Name, st)
						vars, problems = append(vars, v...), append(problems, pr...)
					}
				}
			case *ast.StructType:
				// Анонимная структура (`var c struct{…}` под envconf.Load) — те же переменные.
				if !named[n] && hasEnvTag(n) {
					v, pr := p.structVars(pkgs, f, "struct", n)
					vars, problems = append(vars, v...), append(problems, pr...)
				}
			case *ast.CallExpr:
				if name, ok := envRead(n, osName); ok && !allow[name] {
					vars = append(vars, envVar{name: name, pos: p.pos(n), dir: p.dir})
				}
			}
			return true
		})
	}
	return vars, problems
}

// structVars — теги структуры; вложенный конфиг без своих тегов проверяется вместе
// с ней: ридер ENV идёт в него рекурсивно.
func (p *pkg) structVars(pkgs map[string]*pkg, f *ast.File, name string, st *ast.StructType) (vars []envVar, problems []string) {
	for _, field := range st.Fields.List {
		tag := reflect.StructTag("")
		if field.Tag != nil {
			if s, err := strconv.Unquote(field.Tag.Value); err == nil {
				tag = reflect.StructTag(s)
			}
		}
		if env := tag.Get("envconfig"); env != "" {
			vars = append(vars, envVar{name: env, def: tag.Get("default"), zero: zeroValue(field.Type), pos: p.pos(field), dir: p.dir})
			continue
		}
		if tag.Get("ignored") == "true" || !exported(field) {
			continue
		}
		if sub := p.container(pkgs, f, field.Type); sub != nil {
			if !hasEnvTag(sub.st) {
				v, pr := sub.pkg.structVars(pkgs, f, name+"."+fieldName(field), sub.st)
				vars, problems = append(vars, v...), append(problems, pr...)
			}
			continue
		}
		problems = append(problems, fmt.Sprintf("%s: поле %s.%s без envconfig-тега в конфиге с тегами — ридер ENV его отвергает; нужен тег или ignored:\"true\"",
			p.pos(field), name, fieldName(field)))
	}
	return vars, problems
}

type structRef struct {
	pkg *pkg
	st  *ast.StructType
}

// container — структура модуля, в которую ридер ENV идёт рекурсивно (T, *T, pkg.T).
func (p *pkg) container(pkgs map[string]*pkg, f *ast.File, expr ast.Expr) *structRef {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	switch t := expr.(type) {
	case *ast.Ident:
		if st := p.structs[t.Name]; st != nil {
			return &structRef{p, st}
		}
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)
		if !ok || p.module == "" {
			return nil
		}
		for _, imp := range f.Imports {
			ipath, _ := strconv.Unquote(imp.Path.Value)
			dir, ok := strings.CutPrefix(ipath, p.module+"/")
			if !ok || importName(f, ipath) != x.Name {
				continue
			}
			if other := pkgs[dir]; other != nil && other.structs[t.Sel.Name] != nil {
				return &structRef{other, other.structs[t.Sel.Name]}
			}
		}
	}
	return nil
}

func hasEnvTag(st *ast.StructType) bool {
	for _, field := range st.Fields.List {
		if field.Tag != nil {
			if s, err := strconv.Unquote(field.Tag.Value); err == nil && reflect.StructTag(s).Get("envconfig") != "" {
				return true
			}
		}
	}
	return false
}

func exported(field *ast.Field) bool {
	return ast.IsExported(fieldName(field))
}

func fieldName(field *ast.Field) string {
	if len(field.Names) > 0 {
		return field.Names[0].Name
	}
	t := field.Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch t := t.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// zeroValue — нулевое значение типа поля в записи доков: без default-тега ридер ENV
// оставляет его.
func zeroValue(t ast.Expr) string {
	switch t := t.(type) {
	case *ast.Ident:
		switch {
		case t.Name == "string":
			return `""`
		case t.Name == "bool":
			return "false"
		case strings.HasPrefix(t.Name, "int"), strings.HasPrefix(t.Name, "uint"), strings.HasPrefix(t.Name, "float"):
			return "0"
		}
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok && x.Name == "time" && t.Sel.Name == "Duration" {
			return "0"
		}
	}
	return ""
}

func importName(f *ast.File, ipath string) string {
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == ipath {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return path.Base(ipath)
		}
	}
	return ""
}

func envRead(call *ast.CallExpr, osName string) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || osName == "" || len(call.Args) != 1 || (sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv") {
		return "", false
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != osName {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	return name, err == nil
}

func (p *pkg) pos(n ast.Node) string {
	pos := p.fset.Position(n.Pos())
	return fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
}

// doc — README.md или docs/*.md: таблицы и UPPER_SNAKE-токены.
type doc struct {
	path   string
	owner  string // каталог, который описывает документ
	rows   []row
	tokens map[string]int // токен → первая строка
}

type row struct {
	line    int
	section string // ближайший заголовок «## …» выше таблицы
	cells   []string
	def     int // колонка дефолта таблицы; -1 — её нет
}

func parseDocs(fsys fs.FS) ([]*doc, error) {
	var docs []*doc
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		owner, ok := docOwner(p)
		if !ok {
			return nil
		}
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		docs = append(docs, parseDoc(p, owner, string(src)))
		return nil
	})
	return docs, err
}

func docOwner(p string) (string, bool) {
	dir := path.Dir(p)
	switch {
	case path.Base(p) == "README.md":
		return dir, true
	case path.Ext(p) == ".md" && path.Base(dir) == "docs":
		return path.Dir(dir), true
	}
	return "", false
}

func parseDoc(p, owner, src string) *doc {
	d := &doc{path: p, owner: owner, tokens: map[string]int{}}
	section, header := "", []string(nil)
	defCol := -1
	for i, line := range strings.Split(src, "\n") {
		n := i + 1
		for _, m := range tokenRe.FindAllStringSubmatch(line, -1) {
			if _, seen := d.tokens[m[1]]; !seen {
				d.tokens[m[1]] = n
			}
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			section = trimmed
		}
		if !strings.HasPrefix(trimmed, "|") {
			header, defCol = nil, -1
			continue
		}
		cells := splitRow(trimmed)
		switch {
		case header == nil:
			header, defCol = cells, slices.IndexFunc(cells, defaultRe.MatchString)
		case strings.Trim(trimmed, "|-: ") == "":
		default:
			d.rows = append(d.rows, row{line: n, section: section, cells: cells, def: defCol})
		}
	}
	return d
}

// splitRow режет строку таблицы по «|», кроме экранированного «\|».
func splitRow(line string) []string {
	line = strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|"), `\|`, "\x00")
	cells := strings.Split(line, "|")
	for i, c := range cells {
		cells[i] = strings.ReplaceAll(strings.TrimSpace(c), "\x00", `\|`)
	}
	return cells
}

// docsFor — документы каталога пакета и каталогов выше, кроме корня репо: корневой
// README описывает библиотеку, а не кит.
func docsFor(dir string, docs []*doc) []*doc {
	var out []*doc
	for _, d := range docs {
		if d.owner != "." && (dir == d.owner || strings.HasPrefix(dir, d.owner+"/")) {
			out = append(out, d)
		}
	}
	return out
}

func checkVars(vars []envVar, docs []*doc) []string {
	var problems []string
	undocumented := map[string][]string{}
	for _, v := range vars {
		set := docsFor(v.dir, docs)
		if len(set) == 0 {
			undocumented[v.dir] = append(undocumented[v.dir], v.name)
			continue
		}
		found, withDefault := false, false
		for _, d := range set {
			for _, r := range d.rows {
				if !slices.Contains(r.cells, "`"+v.name+"`") {
					continue
				}
				found = true
				if r.def < 0 || r.def >= len(r.cells) {
					continue
				}
				withDefault = true
				if !sameDefault(v, r.cells[r.def]) {
					problems = append(problems, fmt.Sprintf("%s:%d: %s: дефолт в доках «%s», в коде %s (%s)",
						d.path, r.line, v.name, r.cells[r.def], codeDefault(v), v.pos))
				}
			}
		}
		switch {
		case !found:
			problems = append(problems, fmt.Sprintf("%s: %s — нет строки таблицы в доках %s (README.md, docs/*.md)", v.pos, v.name, set[0].owner))
		case !withDefault:
			problems = append(problems, fmt.Sprintf("%s: %s — у таблицы в доках нет колонки дефолта", v.pos, v.name))
		}
	}
	for _, dir := range slices.Sorted(maps.Keys(undocumented)) {
		problems = append(problems, fmt.Sprintf("%s: нет README.md ни в каталоге, ни выше — переменные без доков: %s",
			dir, strings.Join(undocumented[dir], " ")))
	}
	return problems
}

func codeDefault(v envVar) string {
	if v.def == "" {
		return "без дефолта"
	}
	return "«" + v.def + "»"
}

// sameDefault: ячейка — значение в бэктиках (пояснение после него допустимо); без
// default-тега — «—» с пояснением или нулевое значение типа (`""`, `false`, `0`).
func sameDefault(v envVar, cell string) bool {
	val := cell
	if rest, ok := strings.CutPrefix(cell, "`"); ok {
		if end := strings.Index(rest, "`"); end >= 0 {
			val = rest[:end]
		}
	}
	if v.def != "" {
		return val == v.def
	}
	return strings.HasPrefix(cell, "—") || (v.zero != "" && val == v.zero)
}

func checkTokens(vars []envVar, docs []*doc, allow map[string]bool) []string {
	code := map[string]bool{}
	for _, v := range vars {
		code[v.name] = true
	}
	var problems []string
	for _, d := range docs {
		if d.owner == "." {
			continue
		}
		for _, tok := range slices.Sorted(maps.Keys(d.tokens)) {
			if !code[tok] && !allow[tok] {
				problems = append(problems, fmt.Sprintf("%s:%d: %s — в доках, но не в коде (и не в scripts/envdoc/allow.txt)", d.path, d.tokens[tok], tok))
			}
		}
	}
	return problems
}

func checkRootTable(vars []envVar, docs []*doc, allow map[string]bool) []string {
	byName := map[string]envVar{}
	for _, v := range vars {
		byName[v.name] = v
	}
	var problems []string
	inTable := map[string]bool{}
	for _, d := range docs {
		if d.path != "README.md" {
			continue
		}
		for _, r := range d.rows {
			if !strings.HasPrefix(r.section, rootTableHeading) {
				continue
			}
			for _, m := range tokenRe.FindAllStringSubmatch(r.cells[0], -1) {
				name := m[1]
				inTable[name] = true
				v, ok := byName[name]
				switch {
				case !ok && !allow[name]:
					problems = append(problems, fmt.Sprintf("README.md:%d: %s — переменной нет ни в одном ките", r.line, name))
				case ok && r.def >= 0 && r.def < len(r.cells) && !sameDefault(v, r.cells[r.def]):
					problems = append(problems, fmt.Sprintf("README.md:%d: %s: дефолт в корневой таблице «%s», в коде %s (%s)",
						r.line, name, r.cells[r.def], codeDefault(v), v.pos))
				}
			}
		}
	}
	if len(inTable) == 0 {
		return append(problems, "README.md: секция «"+rootTableHeading+"» не найдена или без таблицы")
	}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		if endpointRe.MatchString(name) && !inTable[name] {
			problems = append(problems, fmt.Sprintf("%s: %s — endpoint-переменная не в корневой таблице § Per-concern endpoints", byName[name].pos, name))
		}
	}
	return problems
}
