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
	// fromEnds — предложения, которыми кончается список FROM.
	fromEnds = []string{"where", "group", "having", "order", "limit", "offset", "returning", "union", "intersect", "except", "window", "for", "set"}
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

// queriedTables — имена в позиции таблицы: после FROM запроса, JOIN, INTO, UPDATE, USING и через
// запятую в списке FROM, в том числе после JOIN … ON …. Колонка с именем таблицы (orders.notes),
// FROM внутри выражения (TRIM(… FROM notes), IS DISTINCT FROM notes) и слово после FOR UPDATE и
// DO UPDATE — не обращение к таблице.
func queriedTables(sql string) []string {
	type scope struct{ query, from bool } // уровень скобок: идёт запрос, открыт список FROM
	scopes := []scope{{}}
	var names []string
	table, prev := false, ""
	for _, tok := range sqlToken.FindAllString(sql, -1) {
		cur := &scopes[len(scopes)-1]
		word := strings.ToLower(tok)
		switch {
		case tok == "(":
			scopes, table = append(scopes, scope{}), false
		case tok == ")":
			scopes, table = scopes[:max(len(scopes)-1, 1)], false
		case tok == ";":
			scopes, table = []scope{{}}, false
		case tok == ",":
			table = cur.from
		case table && word != "only" && word != "lateral":
			names, table = append(names, strings.Trim(word[strings.LastIndex(word, ".")+1:], `"`)), false
		case word == "select" || word == "delete":
			cur.query = true
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
