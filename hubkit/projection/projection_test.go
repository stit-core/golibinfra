package projection_test

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/hubkit/projection"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

// Схема применяется testcontainers-Postgres'ом: шаблон, который не
// накатывается, не эталон.
func TestMigrations_Apply(t *testing.T) {
	pg := testkit.StartPostgres(t, testkit.WithMigrations(projection.Migrations))
	pool := pg.Pool(t)

	want := []string{
		"hub_companies", "hub_projects", "hub_employees",
		"hub_entity_visibility", "hub_sync_cursors",
	}
	for _, table := range want {
		var exists bool
		err := pool.QueryRow(
			t.Context(),
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("таблица %s не создана", table)
		}
	}
}

// Каждый эталонный запрос обязан быть валиден против эталонной схемы.
func TestQueries_ValidAgainstSchema(t *testing.T) {
	pg := testkit.StartPostgres(t, testkit.WithMigrations(projection.Migrations))
	pool := pg.Pool(t)

	files, err := fs.Glob(projection.QueriesFS, "queries/*.sql")
	if err != nil {
		t.Fatalf("glob queries: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("нет эталонных запросов")
	}
	for _, file := range files {
		raw, err := fs.ReadFile(projection.QueriesFS, file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for name, stmt := range statements(t, string(raw)) {
			t.Run(name, func(t *testing.T) {
				if _, err := pool.Exec(t.Context(), fmt.Sprintf("PREPARE %s AS %s", strings.ToLower(name), stmt)); err != nil {
					t.Fatalf("%s: %v", file, err)
				}
			})
		}
	}
}

// Курсор нового ресурса отсутствует в таблице: CursorStore обязан читать это
// как 0, иначе первый тик падает вместо старта фида с начала.
func TestSyncCursor_MissingRowIsZero(t *testing.T) {
	pg := testkit.StartPostgres(t, testkit.WithMigrations(projection.Migrations))
	pool := pg.Pool(t)

	var cursor int64
	err := pool.QueryRow(t.Context(), `SELECT cursor FROM hub_sync_cursors WHERE resource = $1`, "companies").Scan(&cursor)
	if err == nil {
		t.Fatal("ожидали отсутствие строки курсора")
	}
	if _, err := pool.Exec(
		t.Context(),
		`INSERT INTO hub_sync_cursors (resource, cursor, updated_at) VALUES ($1, $2, NOW())
		 ON CONFLICT (resource) DO UPDATE SET cursor = EXCLUDED.cursor, updated_at = NOW()`,
		"companies", 7,
	); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT cursor FROM hub_sync_cursors WHERE resource = $1`, "companies").Scan(&cursor); err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if cursor != 7 {
		t.Fatalf("cursor = %d, want 7", cursor)
	}
}

// Проект приезжает своим тиком, независимо от компании: FK шаблона отвергает
// запись до появления её компании (документированное ограничение шаблона).
func TestProjects_ForeignKeyOnCompany(t *testing.T) {
	pg := testkit.StartPostgres(t, testkit.WithMigrations(projection.Migrations))
	pool := pg.Pool(t)
	ctx := context.Background()

	const (
		companyID = "01980000-0000-7000-8000-000000000001"
		projectID = "01980000-0000-7000-8000-000000000002"
	)
	_, err := pool.Exec(ctx,
		`INSERT INTO hub_projects (id, company_id, name, slug, created_at, updated_at, updated_seq)
		 VALUES ($1, $2, 'P', 'p', NOW(), NOW(), 1)`, projectID, companyID)
	if err == nil {
		t.Fatal("FK на hub_companies не сработал")
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO hub_companies (id, name, slug, created_at, updated_at, updated_seq)
		 VALUES ($1, 'C', 'c', NOW(), NOW(), 1)`, companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO hub_projects (id, company_id, name, slug, created_at, updated_at, updated_seq)
		 VALUES ($1, $2, 'P', 'p', NOW(), NOW(), 1)`, projectID, companyID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
}

var (
	nameRE   = regexp.MustCompile(`(?m)^--\s*name:\s*(\w+)\s*:\w+\s*$`)
	sqlcArg  = regexp.MustCompile(`sqlc\.n?arg\(\s*\w+\s*\)`)
	paramRE  = regexp.MustCompile(`\$(\d+)`)
	commentR = regexp.MustCompile(`(?m)^--.*$`)
)

// statements разбирает sqlc-файл на именованные запросы, подставляя вместо
// sqlc.arg() позиционные параметры: PREPARE проверяет их против схемы.
func statements(t *testing.T, raw string) map[string]string {
	t.Helper()
	idx := nameRE.FindAllStringSubmatchIndex(raw, -1)
	out := make(map[string]string, len(idx))
	for i, loc := range idx {
		name := raw[loc[2]:loc[3]]
		end := len(raw)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		body := commentR.ReplaceAllString(raw[loc[1]:end], "")
		out[name] = numberArgs(strings.TrimRight(strings.TrimSpace(body), ";"))
	}
	return out
}

func numberArgs(stmt string) string {
	next := 0
	for _, m := range paramRE.FindAllStringSubmatch(stmt, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > next {
			next = n
		}
	}
	return sqlcArg.ReplaceAllStringFunc(stmt, func(string) string {
		next++
		return "$" + strconv.Itoa(next)
	})
}
