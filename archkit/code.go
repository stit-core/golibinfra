package archkit

import (
	"go/ast"
	"go/types"
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

// identityClosures: `func(uc *user.Usecase) auth.Port { return uc }` в module/ отдаёт потребителю
// чужие ошибки и типы мимо маппинга.
func (t *tree) identityClosures() {
	for _, f := range t.files {
		if !f.domainCode() || f.sub != "module" {
			continue
		}
		byName := f.importsByName()
		ast.Inspect(f.syntax, func(n ast.Node) bool {
			param, result, ok := identityFunc(n)
			if !ok {
				return true
			}
			if from, _, ok := splitDomain(t.name(byName[typePackage(param)])); ok && from != f.domain {
				t.add(f.path+" returns "+types.ExprString(param)+" as "+types.ExprString(result), whyClosure)
			}
			return true
		})
	}
}

// identityFunc распознаёт функцию, которая отдаёт свой параметр как есть — func(x T) R { return x }
// или func(x T) (R, error) { return x, nil } (конструктор fx с ошибкой), — и возвращает типы T и R.
// Другие параметры рядом и код после return замыкание не прячут.
func identityFunc(n ast.Node) (param, result ast.Expr, ok bool) {
	var fn *ast.FuncType
	var body *ast.BlockStmt
	switch f := n.(type) {
	case *ast.FuncLit:
		fn, body = f.Type, f.Body
	case *ast.FuncDecl:
		fn, body = f.Type, f.Body
	default:
		return nil, nil, false
	}
	if body == nil || len(body.List) == 0 || fn.Results == nil {
		return nil, nil, false
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	results := fn.Results.List
	switch {
	case !ok || len(ret.Results) != len(results) || len(results) > 2:
		return nil, nil, false
	case len(results) == 2 && (!isIdent(results[1].Type, "error") || !isIdent(ret.Results[1], "nil")):
		return nil, nil, false
	}
	for _, p := range fn.Params.List {
		for _, name := range p.Names {
			if isIdent(ret.Results[0], name.Name) {
				return p.Type, results[0].Type, true
			}
		}
	}
	return nil, nil, false
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}

// typePackage — имя пакета в типе вида pkg.T или *pkg.T; "" — тип без пакета.
func typePackage(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}
