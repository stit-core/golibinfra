package tzkit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/testkit"
	"git.stit.tech/stit-core/golibinfra/tzkit"
)

// Интероп с настоящим Postgres: Day и WallTime уходят аргументами и читаются
// Scan'ом без обёрток. Прямые вызовы DateValue/ScanTime этого не ловят —
// кодирование и выбор плана делает pgx, и смена его версии проходит мимо них.
func TestPgxInterop(t *testing.T) {
	pg := testkit.StartPostgres(t)
	db := pg.DB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	if _, err := db.Exec(ctx, `CREATE TABLE schedule (
		id     INT PRIMARY KEY,
		day    DATE NOT NULL,
		opens  TIME NOT NULL,
		until  DATE,
		closes TIME
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Зона сессии восточнее UTC: календарный день не должен от неё зависеть.
	if _, err := db.Exec(ctx, `SET TIME ZONE 'Pacific/Kiritimati'`); err != nil {
		t.Fatalf("set time zone: %v", err)
	}

	day := tzkit.MustParseDay("2026-08-08")
	opens := tzkit.MustParseWallTime("17:00")
	until := tzkit.MustParseDay("2026-12-31")

	if _, err := db.Exec(ctx,
		`INSERT INTO schedule (id, day, opens, until, closes) VALUES ($1, $2, $3, $4, $5)`,
		1, day, opens, until, opens,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Нулевой Day и nil-указатели ложатся NULL'ами.
	if _, err := db.Exec(ctx,
		`INSERT INTO schedule (id, day, opens, until, closes) VALUES ($1, $2, $3, $4, $5)`,
		2, day, opens, tzkit.Day{}, tzkit.PgTimePtr(nil),
	); err != nil {
		t.Fatalf("insert with nulls: %v", err)
	}

	var (
		gotDay    tzkit.Day
		gotOpens  tzkit.WallTime
		gotUntil  *tzkit.Day
		gotCloses *tzkit.WallTime
	)
	if err := db.QueryRow(ctx,
		`SELECT day, opens, until, closes FROM schedule WHERE id = 1`,
	).Scan(&gotDay, &gotOpens, &gotUntil, &gotCloses); err != nil {
		t.Fatalf("scan row 1: %v", err)
	}
	if gotDay != day {
		t.Errorf("day = %s, want %s", gotDay, day)
	}
	if gotOpens != opens {
		t.Errorf("opens = %s, want %s", gotOpens, opens)
	}
	if gotUntil == nil || *gotUntil != until {
		t.Errorf("until = %v, want %s", gotUntil, until)
	}
	if gotCloses == nil || *gotCloses != opens {
		t.Errorf("closes = %v, want %s", gotCloses, opens)
	}

	// NULL: Day читается нулевым, необязательный WallTime — только в **WallTime.
	var nullDay tzkit.Day
	if err := db.QueryRow(ctx,
		`SELECT until, closes FROM schedule WHERE id = 2`,
	).Scan(&nullDay, &gotCloses); err != nil {
		t.Fatalf("scan row 2: %v", err)
	}
	if !nullDay.IsZero() {
		t.Errorf("NULL DATE = %s, want the zero Day", nullDay)
	}
	if gotCloses != nil {
		t.Errorf("NULL TIME = %v, want nil", gotCloses)
	}

	// Day как аргумент сравнения: значение в базе равно тому, что кодирует pgx.
	var matches int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM schedule WHERE day = $1 AND opens = $2`, day, opens,
	).Scan(&matches); err != nil {
		t.Fatalf("count: %v", err)
	}
	if matches != 2 {
		t.Errorf("rows matching (day, opens) = %d, want 2", matches)
	}

	// Postgres допускает TIME '24:00:00' и секунды — WallTime нет.
	var truncated tzkit.WallTime
	if err := db.QueryRow(ctx, `SELECT TIME '17:00:45.123456'`).Scan(&truncated); err != nil {
		t.Fatalf("scan TIME with seconds: %v", err)
	}
	if truncated != opens {
		t.Errorf("TIME '17:00:45.123456' = %s, want %s", truncated, opens)
	}
	var overflow tzkit.WallTime
	if err := db.QueryRow(ctx, `SELECT TIME '24:00:00'`).Scan(&overflow); err == nil {
		t.Errorf("TIME '24:00:00' scanned as %s, want an error", overflow)
	}
}
