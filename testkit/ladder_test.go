package testkit

import (
	"strings"
	"testing"
	"testing/fstest"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

func TestMigrationLadder_RequiresSources(t *testing.T) {
	tb := runOutcome(t, func(tb testing.TB) { MigrationLadder(tb) })
	if tb.outcome != "fail" || !strings.Contains(tb.msg, "needs WithMigrations") {
		t.Fatalf("MigrationLadder without sources: outcome %q %q, want fail naming WithMigrations", tb.outcome, tb.msg)
	}
}

// Лесенка идёт по версиям: down, который не возвращает схему своего up, ловится,
// даже когда следующий down сносит таблицу целиком и «всё вниз — всё вверх» зелёный.
func TestMigrationLadder(t *testing.T) {
	pg := StartPostgres(t)
	pool := pg.Pool(t)
	tests := []struct {
		name  string
		files map[string]string
		fail  string // фрагмент отказа; пусто — лесенка проходит
	}{
		{
			name: "reversible",
			files: map[string]string{
				"0001_t.up.sql":   "CREATE TABLE ok_t (id INT PRIMARY KEY, at TIMESTAMPTZ); CREATE INDEX ok_id_idx ON ok_t (id);",
				"0001_t.down.sql": "DROP TABLE ok_t;",
				"0002_t.up.sql":   "DROP INDEX ok_id_idx; CREATE INDEX ok_at_idx ON ok_t (at);",
				"0002_t.down.sql": "DROP INDEX ok_at_idx; CREATE INDEX ok_id_idx ON ok_t (id);",
			},
		},
		{
			name: "down_forgets_dropped_index",
			files: map[string]string{
				"0001_t.up.sql":   "CREATE TABLE fi_t (id INT PRIMARY KEY, at TIMESTAMPTZ); CREATE INDEX fi_id_idx ON fi_t (id);",
				"0001_t.down.sql": "DROP TABLE fi_t;",
				"0002_t.up.sql":   "DROP INDEX fi_id_idx; CREATE INDEX fi_at_idx ON fi_t (at);",
				"0002_t.down.sql": "DROP INDEX fi_at_idx;",
			},
			fail: "down 2 must restore the schema it started from:\n- index CREATE INDEX fi_id_idx",
		},
		{
			name: "broken_down",
			files: map[string]string{
				"0001_t.up.sql":   "CREATE TABLE bd_t (id INT PRIMARY KEY);",
				"0001_t.down.sql": "DROP TABLE bd_t;",
				"0002_t.up.sql":   "CREATE INDEX bd_id_idx ON bd_t (id);",
				"0002_t.down.sql": "DROP INDEX bd_id_idx_typo;",
			},
			fail: "ladder broken_down: down 2:",
		},
		{
			name: "down_leaves_sequence",
			files: map[string]string{
				"0001_t.up.sql":   "CREATE TABLE ls_t (id INT); CREATE SEQUENCE ls_seq;",
				"0001_t.down.sql": "DROP TABLE ls_t;",
			},
			fail: "down 1 must restore the schema it started from:\n+ relation public.ls_seq",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := fstest.MapFS{}
			for name, sql := range tt.files {
				files[name] = &fstest.MapFile{Data: []byte(sql)}
			}
			src := pgkit.Source{Name: tt.name, FS: files, Dir: "."}

			tb := runOutcome(t, func(tb testing.TB) { ladder(tb, pg.DSN, pool, src) })
			switch {
			case tt.fail == "" && tb.outcome != "":
				t.Fatalf("ladder of a reversible source: %s %s", tb.outcome, tb.msg)
			case tt.fail != "" && (tb.outcome != "fail" || !strings.Contains(tb.msg, tt.fail)):
				t.Fatalf("ladder: outcome %q, message:\n%s\nwant failure containing %q", tb.outcome, tb.msg, tt.fail)
			}
		})
	}
}
