package archkit

import (
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"
)

const whyTables = "a table belongs to one domain, the others go through a port"

var (
	createTable = regexp.MustCompile(`(?i)\bcreate\s+table\s+(?:if\s+not\s+exists\s+)?(?:"?public"?\.)?"?(\w+)`)
	// sqlToken — слово (с кавычками и схемой) или скобка, запятая, точка с запятой: остальное для
	// поиска таблиц не нужно.
	sqlToken = regexp.MustCompile(`"?\w+"?(?:\."?\w+"?)?|[(),;]`)
	// fromEnds — слова, которыми кончается список FROM; все зарезервированы — псевдонимом таблицы
	// такое слово не встанет. DO закрывает список перед ON CONFLICT … DO UPDATE SET a = 1, b = 2.
	fromEnds = []string{"where", "group", "having", "order", "limit", "offset", "returning", "union", "intersect", "except", "window", "for", "do"}
	// lockClauses — слова, после которых UPDATE — не оператор: FOR UPDATE, FOR NO KEY UPDATE, DO UPDATE.
	lockClauses = []string{"for", "key", "do"}
	sqlNoise    = regexp.MustCompile(`--[^\n]*|/\*[\s\S]*?\*/|'(?:[^']|'')*'`)
)

// loadSQL читает таблицы приложения и запросы доменов; запросы при миграциях без таблиц — отказ:
// правило своих таблиц не проверило бы ничего.
func (t *tree) loadSQL(fsys fs.FS, domains []string) error {
	migrations, err := sqlText(fsys, "migrations/*.up.sql")
	if err != nil {
		return err
	}
	for _, m := range createTable.FindAllStringSubmatch(migrations, -1) {
		if name := strings.ToLower(m[1]); !slices.Contains(t.tables, name) {
			t.tables = append(t.tables, name)
		}
	}
	for _, d := range domains {
		sql, err := sqlText(fsys, domainDir+"/"+d+"/infra/postgres/queries/*.sql")
		if err != nil {
			return err
		}
		if sql != "" {
			t.queries[d] = sql
		}
	}
	if len(t.queries) > 0 && len(t.tables) == 0 {
		return errors.New("domains have queries, but migrations/*.up.sql create no tables: the rule of own tables would check nothing")
	}
	return nil
}

// ownTables: таблица приложения (CREATE TABLE в migrations/) принадлежит одному домену; запрос
// соседа к ней — связь в обход порта.
func (t *tree) ownTables() {
	queriedBy := map[string][]string{}
	for _, d := range t.domains() {
		used := queriedTables(t.queries[d])
		for _, table := range t.tables {
			if slices.Contains(used, table) {
				queriedBy[table] = append(queriedBy[table], d)
			}
		}
	}
	for _, table := range t.tables {
		if ds := queriedBy[table]; len(ds) > 1 {
			t.add("table "+table+" is queried by domains "+strings.Join(ds, ", "), whyTables)
		}
	}
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

// queriedTables — имена в позиции таблицы: после FROM запроса, JOIN, INTO, UPDATE, USING и через
// запятую в списке FROM, в том числе после JOIN … ON … и в скобках соединения (a JOIN b). Колонка с
// именем таблицы (orders.notes), FROM внутри выражения (TRIM(… FROM notes), IS DISTINCT FROM notes),
// слово после FOR UPDATE и DO UPDATE, столбцы JOIN … USING (…) и имя CTE своего оператора — не обращение
// к таблице; в теле CTE без RECURSIVE его имя — таблица.
func queriedTables(sql string) []string {
	scopes := []sqlScope{{}}
	var names []string
	table, prev := false, ""
	for _, tok := range sqlToken.FindAllString(sql, -1) {
		cur := &scopes[len(scopes)-1]
		word := strings.ToLower(tok)
		if cur.with != cteNone && cur.with != cteOpen && cur.withList(word) {
			prev = word
			continue
		}
		if table && (word == "select" || word == "values" || word == "with") {
			table = false
		}
		switch {
		case tok == "(":
			// Скобка в позиции таблицы — подзапрос или соединение (a JOIN b): тот же список FROM.
			inFrom := table && prev != "using"
			scopes, table = append(scopes, sqlScope{from: inFrom}), inFrom
		case tok == ")":
			scopes, table = scopes[:max(len(scopes)-1, 1)], false
			if up := &scopes[len(scopes)-1]; up.with == cteOpen {
				if !up.recursive {
					up.ctes = append(up.ctes, up.cte)
				}
				up.with = cteNext
			}
		case tok == ";":
			scopes, table = []sqlScope{{}}, false
		case tok == ",":
			table = cur.from
		case table && word != "only" && word != "lateral":
			if name := relName(word); strings.Contains(word, ".") || !slices.ContainsFunc(scopes, func(s sqlScope) bool { return slices.Contains(s.ctes, name) }) {
				names = append(names, name)
			}
			table = false
		case word == "with":
			cur.with, cur.recursive = cteName, false
		case word == "select" || word == "delete":
			cur.query, cur.from = true, false
		case word == "values":
			cur.from = false
		case word == "from" && cur.query && prev != "distinct":
			cur.from, table = true, true
		case word == "join" || word == "into" || word == "using" || word == "update" && !slices.Contains(lockClauses, prev):
			cur.query, table = true, true
		case slices.Contains(fromEnds, word):
			cur.from = false
		}
		prev = word
	}
	return names
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

// sqlText — SQL файлов glob без комментариев и строк.
func sqlText(fsys fs.FS, glob string) (string, error) {
	files, err := fs.Glob(fsys, glob)
	if err != nil {
		return "", err
	}
	var sql strings.Builder
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return "", err
		}
		sql.WriteString(sqlNoise.ReplaceAllString(string(data), " ") + "\n")
	}
	return sql.String(), nil
}
