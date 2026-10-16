package archkit

import (
	"cmp"
	"errors"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

const whyTables = "a table belongs to one domain, the others go through a port"

var (
	// fromEnds — слова, которыми кончается список FROM; все зарезервированы — псевдонимом таблицы
	// такое слово не встанет. DO закрывает список перед ON CONFLICT … DO UPDATE SET a = 1, b = 2.
	fromEnds = []string{"where", "group", "having", "order", "limit", "offset", "returning", "union", "intersect", "except", "window", "for", "do"}
	// lockClauses — слова, после которых UPDATE — не оператор: FOR UPDATE, FOR NO KEY UPDATE, DO UPDATE.
	lockClauses = []string{"for", "key", "do"}
	// createModifiers — слова между CREATE и видом объекта: OR REPLACE, TEMP, UNLOGGED, MATERIALIZED.
	createModifiers = []string{"or", "replace", "global", "local", "temp", "temporary", "unlogged", "recursive", "materialized"}
)

// sqlRefs — к чему обращается SQL: имена в позиции таблицы (без имён CTE) и вызванные функции.
type sqlRefs struct{ tables, calls []string }

// funcDefs — перегрузки функции: сигнатура (слова скобок после имени) → к чему обращается определение.
type funcDefs map[string]sqlRefs

func (r sqlRefs) with(more sqlRefs) sqlRefs {
	return sqlRefs{tables: slices.Concat(r.tables, more.tables), calls: slices.Concat(r.calls, more.calls)}
}

// loadSQL читает схему приложения из миграций — таблицы, представления и функции — и обращения запросов
// доменов; запросы при миграциях без таблиц — отказ: правило своих таблиц не проверило бы ничего.
func (t *tree) loadSQL(fsys fs.FS, domains []string) error {
	migrations, err := sqlFiles(fsys, "migrations/*.up.sql")
	if err != nil {
		return err
	}
	for _, file := range migrations {
		t.readSchema(file)
	}
	for _, d := range domains {
		queries, err := sqlFiles(fsys, domainDir+"/"+d+"/infra/postgres/queries/*.sql")
		if err != nil {
			return err
		}
		for _, file := range queries {
			t.queries[d] = t.queries[d].with(readRefs(file))
		}
	}
	if len(t.queries) > 0 && len(t.tables) == 0 {
		return errors.New("domains have queries, but migrations/*.up.sql create no tables: the rule of own tables would check nothing")
	}
	return nil
}

// readSchema читает операторы миграции по порядку: CREATE объявляет таблицу приложения, представление или
// функцию с тем, к чему обращается её определение, DROP снимает представление и функцию, ALTER … RENAME TO
// переносит объект на новое имя; тело DO читается так же.
func (t *tree) readSchema(tokens []sqlToken) {
	for _, st := range statements(tokens) {
		if len(st) > 0 && keyword(st[0], "do") {
			for _, tok := range st {
				if tok.str {
					t.readSchema(lexSQL(tok.text))
				}
			}
			continue
		}
		switch at := slices.IndexFunc(st, func(tok sqlToken) bool { return keyword(tok, "create", "drop", "alter") }); {
		case at < 0:
		case st[at].text == "create":
			t.declareObject(st[at+1:])
		case st[at].text == "drop":
			t.dropObjects(st[at+1:])
		default:
			t.renameObject(st[at+1:])
		}
	}
}

// declareObject разбирает объявление после CREATE: таблица — имя, представление и функция — имя и
// обращения определения. Определение заменяет прежнее того же имени (у функции — той же сигнатуры:
// перегрузки складываются).
func (t *tree) declareObject(st []sqlToken) {
	i := 0
	for i < len(st) && keyword(st[i], createModifiers...) {
		i++
	}
	if i >= len(st) || st[i].str {
		return
	}
	kind := st[i].text
	for i++; i < len(st) && keyword(st[i], "if", "not", "exists"); i++ {
	}
	if i >= len(st) || st[i].str {
		return
	}
	name, rest := relName(st[i].text), st[i+1:]
	switch {
	case kind == "table" && !slices.Contains(t.tables, name):
		t.tables = append(t.tables, name)
	case kind == "view":
		t.views[name] = readRefs(rest)
	case kind == "function" || kind == "procedure":
		if t.funcs[name] == nil {
			t.funcs[name] = funcDefs{}
		}
		t.funcs[name][signature(rest)] = readRefs(functionBody(rest))
	}
}

// dropObjects снимает представления и функции оператора DROP (перечень через запятую). Функция с
// сигнатурой, которой нет среди её объявлений (DROP называет только типы), снимается со всеми
// перегрузками. Таблицы правило не снимает: запрос к снятой таблице не собрал бы sqlc.
func (t *tree) dropObjects(st []sqlToken) {
	i := 0
	if i < len(st) && keyword(st[i], "materialized") {
		i++
	}
	if i >= len(st) || !keyword(st[i], "view", "function", "procedure") {
		return
	}
	kind := st[i].text
	for i++; i < len(st) && keyword(st[i], "if", "exists"); i++ {
	}
	for i < len(st) && !st[i].str && !keyword(st[i], "(", ")", ",", "cascade", "restrict") {
		name, sig := relName(st[i].text), signature(st[i+1:])
		switch _, exact := t.funcs[name][sig]; {
		case kind == "view":
			delete(t.views, name)
		case exact:
			delete(t.funcs[name], sig)
		default:
			delete(t.funcs, name)
		}
		i = afterParens(st, i+1)
		if i < len(st) && keyword(st[i], ",") {
			i++
		}
	}
}

// renameObject разбирает ALTER … RENAME TO: таблица под новым именем — таблица приложения, представление
// и функция (перегрузка той же сигнатуры или все) переходят на новое имя. Определения, которые обращались
// к старому имени, обращаются к новому: представление в PostgreSQL держит объект, а не имя.
func (t *tree) renameObject(st []sqlToken) {
	i := 0
	if i < len(st) && keyword(st[i], "materialized") {
		i++
	}
	if i >= len(st) || !keyword(st[i], "table", "view", "function", "procedure") {
		return
	}
	kind := st[i].text
	for i++; i < len(st) && keyword(st[i], "if", "exists", "only"); i++ {
	}
	if i >= len(st) || st[i].str {
		return
	}
	name, sig := relName(st[i].text), signature(st[i+1:])
	if i = afterParens(st, i+1); i+2 >= len(st) || !keyword(st[i], "rename") || !keyword(st[i+1], "to") || st[i+2].str {
		return
	}
	to := relName(st[i+2].text)
	if kind == "function" || kind == "procedure" {
		moved := t.funcs[name]
		if def, exact := moved[sig]; exact {
			moved = funcDefs{sig: def}
		}
		for s, def := range moved {
			if t.funcs[to] == nil {
				t.funcs[to] = funcDefs{}
			}
			t.funcs[to][s] = def
			delete(t.funcs[name], s)
		}
		t.renameRefs(true, name, to)
		return
	}
	if slices.Contains(t.tables, name) && !slices.Contains(t.tables, to) {
		t.tables = append(t.tables, to)
	}
	if def, ok := t.views[name]; ok {
		t.views[to] = def
		delete(t.views, name)
	}
	t.renameRefs(false, name, to)
}

// renameRefs заменяет имя from на to в обращениях определений представлений и функций — в вызовах
// (calls) или в именах таблиц.
func (t *tree) renameRefs(calls bool, from, to string) {
	swap := func(r sqlRefs) sqlRefs {
		names := &r.tables
		if calls {
			names = &r.calls
		}
		*names = slices.Clone(*names)
		for i, name := range *names {
			if name == from {
				(*names)[i] = to
			}
		}
		return r
	}
	for name, def := range t.views {
		t.views[name] = swap(def)
	}
	for _, overloads := range t.funcs {
		for sig, def := range overloads {
			overloads[sig] = swap(def)
		}
	}
}

// signature — сигнатура функции после её имени: слова скобок через пробел; "" — скобок нет.
func signature(st []sqlToken) string {
	end := afterParens(st, 0)
	words := make([]string, 0, end)
	for _, tok := range st[:end] {
		words = append(words, tok.text)
	}
	return strings.Join(words, " ")
}

// afterParens — позиция после скобок, которые открывает st[i]; i — если там не скобка.
func afterParens(st []sqlToken, i int) int {
	if i >= len(st) || !keyword(st[i], "(") {
		return i
	}
	for depth := 0; i < len(st); i++ {
		switch {
		case keyword(st[i], "("):
			depth++
		case keyword(st[i], ")"):
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return i
}

// functionBody — определение функции токенами: строка после AS (тело на SQL или PL/pgSQL) читается как SQL
// своим оператором; тело RETURN … и BEGIN ATOMIC … END — уже токены.
func functionBody(st []sqlToken) []sqlToken {
	var body []sqlToken
	for i, tok := range st {
		if tok.str && i > 0 && keyword(st[i-1], "as") {
			body = append(append(append(body, sqlToken{text: ";"}), lexSQL(tok.text)...), sqlToken{text: ";"})
			continue
		}
		body = append(body, tok)
	}
	return body
}

// ownTables: таблица приложения (CREATE TABLE в migrations/) принадлежит одному домену; запрос
// соседа к ней — прямо или через представление и функцию из миграций — связь в обход порта.
func (t *tree) ownTables() {
	queriedBy := map[string][]string{}
	through := map[string][]string{} // таблица → «домен through view v»: обход, которым домен до неё дошёл
	for _, d := range t.domains() {
		for table, via := range t.reach(t.queries[d]) {
			queriedBy[table] = append(queriedBy[table], d)
			if via != "" {
				through[table] = append(through[table], d+" through "+via)
			}
		}
	}
	for _, table := range t.tables {
		ds := queriedBy[table]
		if len(ds) < 2 {
			continue
		}
		why := whyTables
		if len(through[table]) > 0 {
			why += " (" + strings.Join(through[table], "; ") + ")"
		}
		t.add("table "+table+" is queried by domains "+strings.Join(ds, ", "), why)
	}
}

// reach — таблицы приложения, к которым обращаются refs, и через что: "" — прямо, иначе представление
// или функция из миграций, через которые обращение до таблицы дошло (их определения — цепочкой).
func (t *tree) reach(refs sqlRefs) map[string]string {
	reached := map[string]string{}
	expanded := map[string]bool{}
	var walk func(refs sqlRefs, via string)
	walk = func(refs sqlRefs, via string) {
		for _, name := range refs.tables {
			if prior, seen := reached[name]; slices.Contains(t.tables, name) && (!seen || prior != "" && via == "") {
				reached[name] = via
			}
			if def, ok := t.views[name]; ok && !expanded["view "+name] {
				expanded["view "+name] = true
				walk(def, cmp.Or(via, "view "+name))
			}
		}
		for _, name := range refs.calls {
			if overloads, ok := t.funcs[name]; ok && !expanded["function "+name] {
				expanded["function "+name] = true
				for _, sig := range slices.Sorted(maps.Keys(overloads)) {
					walk(overloads[sig], cmp.Or(via, "function "+name))
				}
			}
		}
	}
	walk(refs, "")
	return reached
}

// Разбор списка WITH на уровне скобок: имя CTE, его столбцы и AS, тело в скобках, запятая к следующему.
const (
	cteNone = iota
	cteName // после WITH [RECURSIVE] или запятой списка: имя CTE
	cteAs   // после имени: (столбцы) или AS
	cteBody // после AS: [NOT] MATERIALIZED и скобка тела
	cteOpen // тело открыто: его скобка закроет CTE
	cteNext // после тела: запятая — следующее имя
)

// sqlScope — уровень скобок запроса.
type sqlScope struct {
	query, from bool     // идёт запрос, открыт список FROM
	ctes        []string // имена CTE, видимые на этом уровне и глубже
	with        int      // разбор списка WITH этого уровня
	cte         string   // имя CTE, которое разбирается
	recursive   bool     // WITH RECURSIVE: имя CTE видно и в его теле
}

// readRefs — к чему обращается SQL: имена в позиции таблицы — после FROM запроса, JOIN, INTO, UPDATE,
// USING и через запятую в списке FROM, в том числе после JOIN … ON … и в скобках соединения (a JOIN b), — и
// функции, которые он вызывает. Колонка с именем таблицы (orders.notes), FROM внутри выражения (TRIM(… FROM
// notes), IS DISTINCT FROM notes), слово после FOR UPDATE и DO UPDATE, столбцы JOIN … USING (…) и имя CTE
// своего оператора — не обращение к таблице; в теле CTE без RECURSIVE его имя — таблица, цель INSERT, UPDATE,
// DELETE и MERGE — таблица и под одноимённым CTE: её PostgreSQL ищет только в каталоге.
func readRefs(tokens []sqlToken) sqlRefs {
	scopes := []sqlScope{{}}
	var refs sqlRefs
	table, target, prev := false, false, ""
	for _, tok := range tokens {
		if tok.str {
			continue
		}
		cur := &scopes[len(scopes)-1]
		word := tok.text
		if cur.with != cteNone && cur.with != cteOpen && cur.withList(word) {
			prev = word
			continue
		}
		if table && (word == "select" || word == "values" || word == "with") {
			table, target = false, false
		}
		switch {
		case word == "(":
			if prev != "" && !strings.ContainsAny(prev, "(),;") {
				refs.calls = append(refs.calls, relName(prev))
			}
			// Скобка в позиции таблицы — подзапрос или соединение (a JOIN b): тот же список FROM.
			inFrom := table && prev != "using"
			scopes, table, target = append(scopes, sqlScope{from: inFrom}), inFrom, false
		case word == ")":
			scopes, table, target = scopes[:max(len(scopes)-1, 1)], false, false
			if up := &scopes[len(scopes)-1]; up.with == cteOpen {
				if !up.recursive {
					up.ctes = append(up.ctes, up.cte)
				}
				up.with = cteNext
			}
		case word == ";":
			scopes, table, target = []sqlScope{{}}, false, false
		case word == ",":
			table, target = cur.from, false
		case table && word != "only" && word != "lateral":
			if name := relName(word); target || strings.Contains(word, ".") || !slices.ContainsFunc(scopes, func(s sqlScope) bool { return slices.Contains(s.ctes, name) }) {
				refs.tables = append(refs.tables, name)
			}
			table, target = false, false
		case word == "with":
			cur.with, cur.recursive = cteName, false
		case word == "select" || word == "delete" || word == "perform":
			cur.query, cur.from = true, false
		case word == "values":
			cur.from = false
		case word == "from" && cur.query && prev != "distinct":
			cur.from, table, target = true, true, prev == "delete"
		case word == "join" || word == "into" || word == "using" || word == "update" && !slices.Contains(lockClauses, prev):
			cur.query, table, target = true, true, word == "into" || word == "update"
		case slices.Contains(fromEnds, word):
			cur.from = false
		}
		prev = word
	}
	return refs
}

// withList ведёт разбор списка WITH; false — слово не из списка или скобка тела и столбцов, которую
// открывает общий разбор: слово идёт обычным путём.
func (s *sqlScope) withList(word string) bool {
	switch {
	case s.with == cteName && word == "recursive":
		s.recursive = true
	case s.with == cteName && !strings.ContainsAny(word, "(),;"):
		s.cte, s.with = strings.Trim(word, `"`), cteAs
	case s.with == cteAs && word == "as":
		s.with = cteBody
	case s.with == cteBody && (word == "not" || word == "materialized"):
	case s.with == cteBody && word == "(":
		if s.recursive {
			s.ctes = append(s.ctes, s.cte)
		}
		s.with = cteOpen
		return false
	case s.with == cteAs && word == "(":
		return false
	case s.with == cteNext && word == ",":
		s.with = cteName
	default:
		s.with = cteNone
		return false
	}
	return true
}

// relName — имя таблицы из слова SQL: без схемы и кавычек.
func relName(word string) string {
	return strings.Trim(word[strings.LastIndex(word, ".")+1:], `"`)
}

// sqlFiles — токены каждого файла glob в порядке версий: файл — свой разбор, оборванная строка или
// комментарий соседний файл не съедают.
func sqlFiles(fsys fs.FS, glob string) ([][]sqlToken, error) {
	files, err := fs.Glob(fsys, glob)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(files, byVersion)
	var tokens [][]sqlToken
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, lexSQL(string(data)))
	}
	return tokens, nil
}

// byVersion — порядок файлов, в котором мигратор накатывает миграции: по номеру версии в начале имени (2
// раньше 10), файл без номера — после, по имени.
func byVersion(a, b string) int {
	va, okA := version(path.Base(a))
	vb, okB := version(path.Base(b))
	switch {
	case okA && okB && va != vb:
		return cmp.Compare(va, vb)
	case okA != okB && okA:
		return -1
	case okA != okB:
		return 1
	}
	return strings.Compare(a, b)
}

// version — номер версии в начале имени файла миграции.
func version(name string) (uint64, bool) {
	v, err := strconv.ParseUint(name[:len(name)-len(strings.TrimLeft(name, "0123456789"))], 10, 64)
	return v, err == nil
}

// sqlToken — слово SQL (в нижнем регистре, имя в кавычках — с кавычками, имя со схемой — через точку),
// знак ( ) , ; или строка: у неё text — содержимое.
type sqlToken struct {
	text string
	str  bool
}

// keyword — токен — слово из words, а не строка.
func keyword(tok sqlToken, words ...string) bool {
	return !tok.str && slices.Contains(words, tok.text)
}

// lexSQL режет SQL на слова, знаки ( ) , ; и строки ('…', E'…', $$…$$, $тег$…$тег$); параметр $1 — слово,
// комментарии (-- и /* */, и вложенные) и прочие знаки опускает.
func lexSQL(sql string) []sqlToken {
	var tokens []sqlToken
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			i = endOfLine(sql, i)
		case strings.HasPrefix(sql[i:], "/*"):
			i = endOfComment(sql, i)
		case c == '\'':
			text, next := quoted(sql, i, false)
			tokens, i = append(tokens, sqlToken{text: text, str: true}), next
		case c == '$' && dollarTag(sql[i:]) != "":
			text, next := dollarQuoted(sql, i)
			tokens, i = append(tokens, sqlToken{text: text, str: true}), next
		case c == '$' && i+1 < len(sql) && isDigit(sql[i+1]):
			next := i + 1
			for next < len(sql) && isDigit(sql[next]) {
				next++
			}
			tokens, i = append(tokens, sqlToken{text: sql[i:next]}), next
		case c == '"' || isWordByte(c):
			name, next := sqlName(sql, i)
			if name == "e" && next < len(sql) && sql[next] == '\'' {
				text, after := quoted(sql, next, true)
				tokens, i = append(tokens, sqlToken{text: text, str: true}), after
				continue
			}
			tokens, i = append(tokens, sqlToken{text: name}), next
		case strings.IndexByte("(),;", c) >= 0:
			tokens, i = append(tokens, sqlToken{text: string(c)}), i+1
		default:
			i++
		}
	}
	return tokens
}

// statements режет токены на операторы по ; вне тела BEGIN ATOMIC … END: в нём ; — внутри оператора.
func statements(tokens []sqlToken) [][]sqlToken {
	var all [][]sqlToken
	start, atomic := 0, 0
	for i, tok := range tokens {
		switch {
		case keyword(tok, "atomic") && i > 0 && keyword(tokens[i-1], "begin"):
			atomic = 1
		case atomic > 0 && keyword(tok, "case"):
			atomic++
		case atomic > 0 && keyword(tok, "end"):
			atomic--
		case atomic == 0 && keyword(tok, ";"):
			all, start = append(all, tokens[start:i]), i+1
		}
	}
	return append(all, tokens[start:])
}

// sqlName читает имя с позиции i: слово или имя в кавычках, со схемой через точку (и с пробелами вокруг
// неё); возвращает его в нижнем регистре и позицию после него.
func sqlName(sql string, i int) (string, int) {
	var name strings.Builder
	for {
		start := i
		if sql[i] == '"' {
			i = closingQuote(sql, i)
		} else {
			for i < len(sql) && (isWordByte(sql[i]) || sql[i] == '$') {
				i++
			}
		}
		name.WriteString(strings.ToLower(sql[start:i]))
		dot := skipSpaces(sql, i)
		if dot >= len(sql) || sql[dot] != '.' {
			return name.String(), i
		}
		next := skipSpaces(sql, dot+1)
		if next >= len(sql) || sql[next] != '"' && !isWordByte(sql[next]) {
			return name.String(), i
		}
		name.WriteByte('.')
		i = next
	}
}

// closingQuote — позиция после имени в кавычках с позиции i ("" внутри — кавычка).
func closingQuote(sql string, i int) int {
	for i++; i < len(sql); i++ {
		if sql[i] == '"' {
			if i+1 < len(sql) && sql[i+1] == '"' {
				i++
				continue
			}
			return i + 1
		}
	}
	return len(sql)
}

// quoted читает строку '…' с позиции i: два апострофа подряд — апостроф, у E'…' (escapes) ещё \' и \\.
func quoted(sql string, i int, escapes bool) (string, int) {
	var text strings.Builder
	for i++; i < len(sql); i++ {
		switch {
		case escapes && sql[i] == '\\' && i+1 < len(sql):
			i++
			text.WriteByte(sql[i])
		case sql[i] != '\'':
			text.WriteByte(sql[i])
		case i+1 < len(sql) && sql[i+1] == '\'':
			i++
			text.WriteByte('\'')
		default:
			return text.String(), i + 1
		}
	}
	return text.String(), len(sql)
}

// dollarTag — открывающий тег строки в долларах в начале s ($$ или $тег$); "" — не строка: параметр $1
// или знак.
func dollarTag(s string) string {
	j := 1
	for j < len(s) && (s[j] == '_' || isLetter(s[j]) || j > 1 && isDigit(s[j])) {
		j++
	}
	if j < len(s) && s[j] == '$' {
		return s[:j+1]
	}
	return ""
}

// dollarQuoted читает строку $тег$…$тег$ с позиции i: содержимое и позиция после закрывающего тега.
func dollarQuoted(sql string, i int) (string, int) {
	tag := dollarTag(sql[i:])
	start := i + len(tag)
	end := strings.Index(sql[start:], tag)
	if end < 0 {
		return sql[start:], len(sql)
	}
	return sql[start : start+end], start + end + len(tag)
}

func endOfLine(sql string, i int) int {
	if n := strings.IndexByte(sql[i:], '\n'); n >= 0 {
		return i + n
	}
	return len(sql)
}

// endOfComment — позиция после комментария /* … */ с позиции i; вложенный комментарий закрывается своим */.
func endOfComment(sql string, i int) int {
	depth := 0
	for i < len(sql) {
		switch {
		case strings.HasPrefix(sql[i:], "/*"):
			depth, i = depth+1, i+2
		case strings.HasPrefix(sql[i:], "*/"):
			depth, i = depth-1, i+2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(sql)
}

func skipSpaces(sql string, i int) int {
	for i < len(sql) && strings.IndexByte(" \t\r\n", sql[i]) >= 0 {
		i++
	}
	return i
}

// isWordByte — байт слова SQL: буква, цифра, _ и байты не-ASCII букв.
func isWordByte(c byte) bool { return c == '_' || isLetter(c) || isDigit(c) || c >= 0x80 }

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
