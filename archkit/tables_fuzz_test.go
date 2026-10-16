package archkit

import "testing"

// Разбор SQL не падает ни на каком тексте: миграции и запросы потребителя — любые, и правило не роняет
// его тест.
func FuzzSQL(f *testing.F) {
	for _, seed := range []string{
		"", "$", "$$", "$a$", "$1", "'", `E'\`, `"`, `""`, "/*", "/* /*", "--", "a.", "a . ", `"a".`, ".",
		"WITH", "WITH x AS (", "WITH RECURSIVE", ")))", "(((", "CREATE", "CREATE TABLE", "CREATE OR REPLACE",
		"CREATE FUNCTION f() AS $$", "CREATE FUNCTION f() AS", "BEGIN ATOMIC", "BEGIN ATOMIC CASE END END;", "DO $$", "DO",
		"SELECT a FROM t1 JOIN t2 USING (id)", "CREATE VIEW v AS SELECT * FROM t; SELECT * FROM v",
		"CREATE FUNCTION f() RETURNS int AS $x$ SELECT f() $x$; CREATE VIEW v AS SELECT f() FROM v;",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		tokens := lexSQL(sql)
		for _, tok := range tokens {
			if !tok.str && tok.text == "" {
				t.Fatalf("empty word in %q", sql)
			}
		}
		schema := &tree{views: map[string]sqlRefs{}, funcs: map[string]funcDefs{}}
		schema.readSchema(tokens)
		schema.reach(readRefs(tokens))
	})
}
