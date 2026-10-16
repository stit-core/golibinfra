package archkit

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"
)

const (
	whyClock    = "the domain reads time through an injected clockkit.Clock"
	whyDotClock = "the wall clock would hide behind the dot-import"
	whyClosure  = "a port between domains is implemented by a typed adapter in module/ that maps errors and types"
)

// wallClockFuncs — функции time, которые читают или ждут настенные часы.
var wallClockFuncs = []string{"Now", "Since", "Until", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker", "Sleep"}

// wallClock: детерминизм — не-тестовый код домена не трогает настенные часы. Пакет берётся из
// импортов файла: алиас вызов не прячет.
func (t *tree) wallClock() {
	for _, f := range t.files {
		if !f.domainCode() {
			continue
		}
		byName := f.importsByName()
		for i, spec := range f.syntax.Imports {
			if spec.Name != nil && spec.Name.Name == "." && f.imports[i] == "time" {
				t.add(f.path+" dot-imports time", whyDotClock)
			}
		}
		ast.Inspect(f.syntax, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !slices.Contains(wallClockFuncs, sel.Sel.Name) {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && byName[id.Name] == "time" {
				t.add(f.path+" uses time."+sel.Sel.Name, whyClock)
			}
			return true
		})
	}
}

// identityClosures: функция module/, которая отдаёт значение чужого домена — свой параметр, его поле,
// захваченное из объемлющей функции — под типом не из чужого домена (порт, any), привязывает порт без
// адаптера — потребитель получает чужие ошибки и типы мимо маппинга.
func (t *tree) identityClosures() {
	for _, f := range t.files {
		if !f.domainCode() || f.sub != "module" {
			continue
		}
		ast.Inspect(f.syntax, func(n ast.Node) bool {
			if fn, _ := funcParts(n); fn == nil {
				return true
			}
			fl := &flow{t: t, f: f}
			ast.Walk(scopeVisitor{fl: fl}, n)
			for _, c := range fl.passed() {
				t.add(f.path+" returns "+types.ExprString(c.param)+" as "+types.ExprString(c.result), whyClosure)
			}
			return false
		})
	}
}

// passing — параметр чужого домена, который функция возвращает, и тип результата, под которым.
type passing struct{ param, result ast.Expr }

// binding — имя функции module/ (параметр, получатель, результат, переменная) в своей области: видно
// внутри scope с позиции from. held — тип параметра чужого домена, чьё значение имя держит (nil — не
// держит); fields — поля чужого домена, если имя — структура (fx.In и любая другая): поле → тип.
type binding struct {
	name   string
	scope  ast.Node
	from   token.Pos
	held   ast.Expr
	fields map[string]ast.Expr
}

// assignment — значение, которое получает имя: to — объявленное тут же, иначе имя ищется в позиции pos.
type assignment struct {
	to    *binding
	name  string
	pos   token.Pos
	value ast.Expr
}

// fnScope — функция или литерал функции: сигнатура и именованные результаты.
type fnScope struct {
	typ   *ast.FuncType
	named []*binding
}

// flow — функция module/ с вложенными литералами: имена по областям видимости, присваивания и return.
// Значение идёт по присваиваниям без порядка исполнения: имя, которому его присвоили где угодно в
// своей области, держит его везде в ней.
type flow struct {
	t        *tree
	f        goFile
	bindings []*binding
	assigns  []assignment
	returns  map[*ast.ReturnStmt]*fnScope
	literals []*ast.CompositeLit
}

// scopeVisitor обходит функцию и раскладывает имена по областям: функция, блок, if, for, switch, case.
type scopeVisitor struct {
	fl    *flow
	scope ast.Node
	fn    *fnScope
}

func (v scopeVisitor) Visit(n ast.Node) ast.Visitor {
	switch x := n.(type) {
	case *ast.FuncDecl, *ast.FuncLit:
		v.fl.function(x)
		return nil
	case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.SwitchStmt, *ast.SelectStmt, *ast.CaseClause, *ast.CommClause:
		return scopeVisitor{fl: v.fl, scope: n, fn: v.fn}
	case *ast.RangeStmt:
		if x.Tok == token.DEFINE {
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if id, ok := e.(*ast.Ident); ok {
					v.fl.declare(id.Name, x, x.Body.Pos())
				}
			}
		}
		return scopeVisitor{fl: v.fl, scope: x, fn: v.fn}
	case *ast.TypeSwitchStmt:
		inner := scopeVisitor{fl: v.fl, scope: x, fn: v.fn}
		if x.Init != nil {
			ast.Walk(inner, x.Init)
		}
		if as, ok := x.Assign.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if id, ok := as.Lhs[0].(*ast.Ident); ok {
				v.fl.assigns = append(v.fl.assigns, assignment{to: v.fl.declare(id.Name, x, x.Body.Pos()), value: as.Rhs[0]})
			}
		} else if x.Assign != nil {
			ast.Walk(inner, x.Assign)
		}
		ast.Walk(inner, x.Body)
		return nil
	case *ast.AssignStmt:
		v.fl.assign(x, v.scope)
	case *ast.ValueSpec:
		for i, id := range x.Names {
			b := v.fl.declare(id.Name, v.scope, x.End())
			if i < len(x.Values) && len(x.Values) == len(x.Names) {
				v.fl.assigns = append(v.fl.assigns, assignment{to: b, value: x.Values[i]})
			}
		}
	case *ast.ReturnStmt:
		v.fl.returns[x] = v.fn
	case *ast.CompositeLit:
		v.fl.literals = append(v.fl.literals, x)
	}
	return v
}

// function объявляет параметры и именованные результаты функции n в её области и обходит тело: верхний
// блок тела — та же область, что параметры.
func (fl *flow) function(n ast.Node) {
	ft, body := funcParts(n)
	fn := &fnScope{typ: ft}
	params := ft.Params.List
	if decl, ok := n.(*ast.FuncDecl); ok && decl.Recv != nil {
		params = append(slices.Clone(decl.Recv.List), params...)
	}
	for _, p := range params {
		for _, name := range p.Names {
			b := fl.declare(name.Name, n, n.Pos())
			if fl.t.foreign(fl.f, fl.f.domain, p.Type, 0) {
				b.held = p.Type
			}
			b.fields = fl.t.foreignFields(fl.f, p.Type)
		}
	}
	if ft.Results != nil {
		for _, r := range ft.Results.List {
			for _, name := range r.Names {
				fn.named = append(fn.named, fl.declare(name.Name, n, n.Pos()))
			}
		}
	}
	if body == nil {
		return
	}
	if fl.returns == nil {
		fl.returns = map[*ast.ReturnStmt]*fnScope{}
	}
	inner := scopeVisitor{fl: fl, scope: n, fn: fn}
	for _, st := range body.List {
		ast.Walk(inner, st)
	}
}

// declare заводит имя в области scope; имя, уже объявленное в ней же (a, b := … с прежним a), — то же.
func (fl *flow) declare(name string, scope ast.Node, from token.Pos) *binding {
	for _, b := range fl.bindings {
		if b.name == name && b.scope == scope {
			return b
		}
	}
	b := &binding{name: name, scope: scope, from: from}
	fl.bindings = append(fl.bindings, b)
	return b
}

// assign записывает присваивания оператора: := объявляет новые имена в области scope, = присваивает
// видимым; v, ok := x.(T) — v получает x.
func (fl *flow) assign(s *ast.AssignStmt, scope ast.Node) {
	names, values := s.Lhs, s.Rhs
	if _, commaOK := ast.Unparen(single(values)).(*ast.TypeAssertExpr); commaOK && len(names) == 2 {
		names = names[:1]
	}
	for i, e := range s.Lhs {
		id, ok := e.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		var to *binding
		if s.Tok == token.DEFINE {
			to = fl.declare(id.Name, scope, s.End())
		}
		if i < len(names) && len(names) == len(values) {
			fl.assigns = append(fl.assigns, assignment{to: to, name: id.Name, pos: s.Pos(), value: values[i]})
		}
	}
}

// resolve — объявление имени name, которое видно в позиции pos: самая внутренняя область, где оно
// объявлено до pos; nil — имя функции не принадлежит.
func (fl *flow) resolve(name string, pos token.Pos) *binding {
	var found *binding
	for _, b := range fl.bindings {
		if b.name != name || pos < b.from || pos < b.scope.Pos() || pos >= b.scope.End() {
			continue
		}
		if found == nil || b.scope.Pos() > found.scope.Pos() {
			found = b
		}
	}
	return found
}

// passed — значения чужого домена, которые функции потока отдают под типом не из чужого домена: в
// любом return и любой позиции результата, как есть, в скобках, приведением к интерфейсу или чужому
// типу, утверждением типа, через переменную или именованный результат, — и полем литерала (fx.Out,
// встроенное поле).
func (fl *flow) passed() []passing {
	for changed := true; changed; {
		changed = false
		for _, a := range fl.assigns {
			to := a.to
			if to == nil {
				to = fl.resolve(a.name, a.pos)
			}
			if to == nil {
				continue
			}
			if param := fl.holds(a.value); param != nil && to.held == nil {
				to.held, changed = param, true
			}
			if src := fl.source(a.value); src != nil && src.fields != nil && to.fields == nil {
				to.fields, changed = src.fields, true
			}
		}
	}
	var found []passing
	for _, ret := range sortedReturns(fl.returns) {
		fn := fl.returns[ret]
		var results []ast.Expr
		if fn.typ.Results != nil {
			for _, r := range fn.typ.Results.List {
				for range max(len(r.Names), 1) {
					results = append(results, r.Type)
				}
			}
		}
		held := make([]ast.Expr, len(results))
		switch {
		case len(ret.Results) == 0 && len(fn.named) == len(results):
			for i, b := range fn.named {
				held[i] = b.held
			}
		case len(ret.Results) == len(results):
			for i, v := range ret.Results {
				held[i] = fl.holds(v)
			}
		}
		for i, param := range held {
			if param != nil && !fl.t.foreign(fl.f, fl.f.domain, results[i], 0) {
				found = append(found, passing{param: param, result: results[i]})
			}
		}
	}
	for _, lit := range fl.literals {
		found = append(found, fl.literalPassing(lit)...)
	}
	return found
}

// literalPassing — поля литерала, которым досталось значение чужого домена под типом не из чужого домена,
// если поле отдаёт его дальше как есть: поле структуры с fx.Out (fx кладёт его в граф отдельным значением)
// и встроенное поле (его методы — методы литерала).
func (fl *flow) literalPassing(lit *ast.CompositeLit) []passing {
	file, st := fl.t.structOf(fl.f, lit.Type, 0)
	if st == nil {
		return nil
	}
	out := fl.t.embedsFxOut(file, st)
	var fields []structField
	for _, field := range st.Fields.List {
		for _, name := range fieldNames(field) {
			fields = append(fields, structField{name: name, typ: field.Type, embedded: len(field.Names) == 0})
		}
	}
	var found []passing
	for i, elt := range lit.Elts {
		value, at := elt, i
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			key, _ := kv.Key.(*ast.Ident)
			value, at = kv.Value, slices.IndexFunc(fields, func(f structField) bool { return key != nil && f.name == key.Name })
		}
		if at < 0 || at >= len(fields) || !out && !fields[at].embedded {
			continue
		}
		if param := fl.holds(value); param != nil && !fl.t.foreign(file, fl.f.domain, fields[at].typ, 0) {
			found = append(found, passing{param: param, result: fields[at].typ})
		}
	}
	return found
}

// structField — поле структуры: имя (у встроенного — имя типа) и тип.
type structField struct {
	name     string
	typ      ast.Expr
	embedded bool
}

// sortedReturns — return потока в порядке текста: строки отказа не зависят от обхода map.
func sortedReturns(returns map[*ast.ReturnStmt]*fnScope) []*ast.ReturnStmt {
	rets := slices.Collect(maps.Keys(returns))
	slices.SortFunc(rets, func(a, b *ast.ReturnStmt) int { return int(a.Pos() - b.Pos()) })
	return rets
}

// single — единственное выражение списка; nil — их не одно.
func single(exprs []ast.Expr) ast.Expr {
	if len(exprs) != 1 {
		return nil
	}
	return exprs[0]
}

// holds — тип параметра чужого домена, чьё значение выражение отдаёт как есть: имя, которое его держит,
// поле чужого домена у структуры (d.Users), скобки, *&, утверждение типа, приведение к интерфейсу или к
// чужому типу; nil — не отдаёт.
func (fl *flow) holds(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			if b := fl.resolve(x.Name, x.Pos()); b != nil {
				return b.held
			}
			return nil
		case *ast.SelectorExpr:
			if b := fl.source(x.X); b != nil {
				return b.fields[x.Sel.Name]
			}
			return nil
		case *ast.ParenExpr:
			e = x.X
		case *ast.TypeAssertExpr:
			e = x.X
		case *ast.StarExpr:
			addr, isAddr := ast.Unparen(x.X).(*ast.UnaryExpr)
			if !isAddr || addr.Op != token.AND {
				return nil
			}
			e = addr.X
		case *ast.CallExpr:
			if len(x.Args) != 1 || x.Ellipsis.IsValid() || !fl.t.keepsValue(fl.f, x.Fun) {
				return nil
			}
			e = x.Args[0]
		default:
			return nil
		}
	}
}

// source — объявление имени, которое называет выражение (в скобках); nil — не имя функции.
func (fl *flow) source(e ast.Expr) *binding {
	if id, ok := ast.Unparen(e).(*ast.Ident); ok {
		return fl.resolve(id.Name, id.Pos())
	}
	return nil
}

// funcParts — сигнатура и тело функции или литерала функции; у другого узла — nil.
func funcParts(n ast.Node) (*ast.FuncType, *ast.BlockStmt) {
	switch f := n.(type) {
	case *ast.FuncLit:
		return f.Type, f.Body
	case *ast.FuncDecl:
		return f.Type, f.Body
	}
	return nil, nil
}

// keepsValue — приведение к типу e оставляет значение как есть: e — интерфейс, тип чужого домена или
// указатель на него. Приведение к своему или внешнему типу (string(s), order.Status(s)) — новое значение.
func (t *tree) keepsValue(f goFile, e ast.Expr) bool {
	e = ast.Unparen(e)
	if star, ok := e.(*ast.StarExpr); ok {
		return t.foreign(f, f.domain, star.X, 0)
	}
	return t.foreign(f, f.domain, e, 0) || t.isInterface(f, e, 0)
}

// foreignFields — поля чужого домена у структуры e (литерал struct{…} или тип пакета домена): имя поля →
// тип; nil — e не структура.
func (t *tree) foreignFields(f goFile, e ast.Expr) map[string]ast.Expr {
	file, st := t.structOf(f, e, 0)
	if st == nil {
		return nil
	}
	fields := map[string]ast.Expr{}
	for _, field := range st.Fields.List {
		if t.foreign(file, f.domain, field.Type, 0) {
			for _, name := range fieldNames(field) {
				fields[name] = field.Type
			}
		}
	}
	return fields
}

// structOf — структура, которую называет тип e (указатель, скобки и алиасы раскрываются), и файл, в
// чьих импортах читаются типы её полей; nil — не структура или вне разобранных пакетов доменов.
func (t *tree) structOf(f goFile, e ast.Expr, hops int) (goFile, *ast.StructType) {
	if e == nil {
		return f, nil
	}
	switch x := deref(e).(type) {
	case *ast.StructType:
		return f, x
	default:
		if _, decl, ok := t.typeDecl(f, x); ok && hops < maxTypeHops {
			return t.structOf(decl.file, decl.spec.Type, hops+1)
		}
	}
	return f, nil
}

// embedsFxOut — в структуре встроен fx.Out: fx кладёт каждое её поле в граф отдельным значением.
func (t *tree) embedsFxOut(f goFile, st *ast.StructType) bool {
	return slices.ContainsFunc(st.Fields.List, func(field *ast.Field) bool {
		sel, ok := deref(field.Type).(*ast.SelectorExpr)
		if !ok || len(field.Names) != 0 || sel.Sel.Name != "Out" {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && f.importsByName()[pkg.Name] == "go.uber.org/fx"
	})
}

// fieldNames — имена поля; у встроенного — имя его типа (*user.Trusted → Trusted).
func fieldNames(field *ast.Field) []string {
	if len(field.Names) == 0 {
		switch x := deref(field.Type).(type) {
		case *ast.Ident:
			return []string{x.Name}
		case *ast.SelectorExpr:
			return []string{x.Sel.Name}
		case *ast.IndexExpr:
			return fieldNames(&ast.Field{Type: x.X})
		case *ast.IndexListExpr:
			return fieldNames(&ast.Field{Type: x.X})
		}
		return nil
	}
	names := make([]string, 0, len(field.Names))
	for _, name := range field.Names {
		names = append(names, name.Name)
	}
	return names
}

// maxTypeHops — предел раскрытия алиасов: цикл алиасов не компилируется, но разбор его не ждёт.
const maxTypeHops = 16

// foreign — тип e (указатель, скобки и алиасы раскрываются) объявлен в домене, отличном от domain.
func (t *tree) foreign(f goFile, domain string, e ast.Expr, hops int) bool {
	pkg, decl, ok := t.typeDecl(f, deref(e))
	if ok && decl.spec.Assign.IsValid() && hops < maxTypeHops {
		return t.foreign(decl.file, domain, decl.spec.Type, hops+1)
	}
	d, _, inDomain := splitDomain(pkg)
	return inDomain && d != domain
}

// isInterface — e называет интерфейс: литерал interface{…}, any, error или объявление, чей тип —
// интерфейс. Тип вне пакетов доменов (fmt.Stringer) правило не раскрывает.
func (t *tree) isInterface(f goFile, e ast.Expr, hops int) bool {
	e = ast.Unparen(e)
	if _, ok := e.(*ast.InterfaceType); ok {
		return true
	}
	if _, decl, ok := t.typeDecl(f, e); ok {
		return hops < maxTypeHops && t.isInterface(decl.file, decl.spec.Type, hops+1)
	}
	id, ok := e.(*ast.Ident)
	return ok && (id.Name == "any" || id.Name == "error")
}

// typeDecl находит объявление типа, которое называет e в файле f: имя пакета файла или его dot-импорта,
// pkg.Name импорта. pkg — пакет модуля (путь от корня) или путь внешнего пакета; ok — объявление в
// разобранном пакете домена найдено.
func (t *tree) typeDecl(f goFile, e ast.Expr) (pkg string, decl declared, ok bool) {
	switch x := e.(type) {
	case *ast.Ident:
		if decl, ok := t.types[f.pkg][x.Name]; ok {
			return f.pkg, decl, true
		}
		for _, imp := range f.dotImports() {
			rel, _ := t.inModule(imp)
			if decl, ok := t.types[rel][x.Name]; ok {
				return rel, decl, true
			}
		}
	case *ast.SelectorExpr:
		id, _ := x.X.(*ast.Ident)
		if id == nil {
			break
		}
		imp := f.importsByName()[id.Name]
		rel, inModule := t.inModule(imp)
		if !inModule {
			return imp, declared{}, false
		}
		decl, ok := t.types[rel][x.Sel.Name]
		return rel, decl, ok
	case *ast.IndexExpr:
		return t.typeDecl(f, x.X)
	case *ast.IndexListExpr:
		return t.typeDecl(f, x.X)
	}
	return "", declared{}, false
}

// deref снимает с типа скобки и указатели.
func deref(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		default:
			return e
		}
	}
}
