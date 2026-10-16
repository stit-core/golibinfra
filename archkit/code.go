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

// identityClosures: функция module/, которая возвращает свой параметр чужого домена под типом не из
// чужого домена (порт, any), привязывает порт без адаптера — потребитель получает чужие ошибки и типы мимо
// маппинга.
func (t *tree) identityClosures() {
	for _, f := range t.files {
		if f.domainCode() && f.sub == "module" {
			t.closuresIn(f, f.syntax, nil)
		}
	}
}

// closuresIn проверяет функции под узлом n. outer — значения чужого домена функций уровнем выше (имя →
// тип параметра): литерал функции, который их захватил, отдаёт их так же, как свои параметры.
func (t *tree) closuresIn(f goFile, n ast.Node, outer map[string]ast.Expr) {
	ast.Inspect(n, func(n ast.Node) bool {
		fn, body := funcParts(n)
		if body == nil {
			return true
		}
		seen := maps.Clone(outer)
		if seen == nil {
			seen = map[string]ast.Expr{}
		}
		for _, p := range fn.Params.List {
			for _, name := range p.Names {
				delete(seen, name.Name)
				if t.foreign(f, f.domain, p.Type, 0) {
					seen[name.Name] = p.Type
				}
			}
		}
		held := t.holders(f, body, seen)
		for _, c := range t.passedThrough(f, fn, body, held) {
			t.add(f.path+" returns "+types.ExprString(c.param)+" as "+types.ExprString(c.result), whyClosure)
		}
		t.closuresIn(f, body, held)
		return false
	})
}

// passing — параметр чужого домена, который функция возвращает, и тип результата, под которым.
type passing struct{ param, result ast.Expr }

// passedThrough — значения чужого домена из held, которые функция возвращает под типом не из чужого
// домена: в любом return и любой позиции результата, как есть, в скобках, приведением к интерфейсу или
// чужому типу, утверждением типа, через переменную или именованный результат.
func (t *tree) passedThrough(f goFile, fn *ast.FuncType, body *ast.BlockStmt, held map[string]ast.Expr) []passing {
	if fn.Results == nil || len(held) == 0 {
		return nil
	}
	var results, named []ast.Expr
	for _, r := range fn.Results.List {
		if len(r.Names) == 0 {
			results = append(results, r.Type)
		}
		for _, name := range r.Names {
			results, named = append(results, r.Type), append(named, name)
		}
	}
	var found []passing
	inspectBody(body, func(n ast.Node) {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return
		}
		values := ret.Results
		if len(values) == 0 {
			values = named
		}
		if len(values) != len(results) {
			return
		}
		for i, v := range values {
			if param, ok := t.holds(f, v, held); ok && !t.foreign(f, f.domain, results[i], 0) {
				found = append(found, passing{param: param, result: results[i]})
			}
		}
	})
	return found
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

// inspectBody обходит тело функции без вложенных литералов функций: у них свои параметры и return.
func inspectBody(body *ast.BlockStmt, visit func(ast.Node)) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, nested := n.(*ast.FuncLit); nested || n == nil {
			return false
		}
		visit(n)
		return true
	})
}

// holders — имена, под которыми тело держит значения seed (имя → тип параметра): они сами и переменные,
// которым их присвоили (и v из v, ok := x.(T)).
func (t *tree) holders(f goFile, body *ast.BlockStmt, seed map[string]ast.Expr) map[string]ast.Expr {
	held := maps.Clone(seed)
	for grew := len(held) > 0; grew; {
		grew = false
		inspectBody(body, func(n ast.Node) {
			var names, values []ast.Expr
			switch s := n.(type) {
			case *ast.AssignStmt:
				names, values = s.Lhs, s.Rhs
			case *ast.ValueSpec:
				for _, id := range s.Names {
					names = append(names, id)
				}
				values = s.Values
			}
			if _, commaOK := ast.Unparen(single(values)).(*ast.TypeAssertExpr); commaOK && len(names) == 2 {
				names = names[:1]
			}
			if len(names) != len(values) {
				return
			}
			for i, name := range names {
				id, ok := name.(*ast.Ident)
				if !ok || held[id.Name] != nil {
					continue
				}
				if param, ok := t.holds(f, values[i], held); ok {
					held[id.Name], grew = param, true
				}
			}
		})
	}
	return held
}

// single — единственное выражение списка; nil — их не одно.
func single(exprs []ast.Expr) ast.Expr {
	if len(exprs) != 1 {
		return nil
	}
	return exprs[0]
}

// holds — выражение отдаёт значение одного из имён held как есть: само имя, скобки, *&, утверждение
// типа, приведение к интерфейсу или к чужому типу; param — тип параметра, чьё это значение.
func (t *tree) holds(f goFile, e ast.Expr, held map[string]ast.Expr) (param ast.Expr, ok bool) {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			param, ok = held[x.Name]
			return param, ok
		case *ast.ParenExpr:
			e = x.X
		case *ast.TypeAssertExpr:
			e = x.X
		case *ast.StarExpr:
			addr, isAddr := ast.Unparen(x.X).(*ast.UnaryExpr)
			if !isAddr || addr.Op != token.AND {
				return nil, false
			}
			e = addr.X
		case *ast.CallExpr:
			if len(x.Args) != 1 || x.Ellipsis.IsValid() || !t.keepsValue(f, x.Fun) {
				return nil, false
			}
			e = x.Args[0]
		default:
			return nil, false
		}
	}
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
