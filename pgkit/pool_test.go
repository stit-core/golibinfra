package pgkit_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestNewPool_AppliesSessionTimeouts(t *testing.T) {
	pg := testkit.StartPostgres(t)
	cfg := pgkit.PoolConfig{
		DSN:                    pg.DSN,
		MaxConns:               2,
		MinConns:               1,
		MaxConnLifetime:        5 * time.Minute,
		MaxConnIdleTime:        time.Minute,
		HealthCheck:            30 * time.Second,
		ConnectTimeout:         10 * time.Second,
		StatementTimeout:       7 * time.Second,
		IdleInTxSessionTimeout: 11 * time.Second,
		LockTimeout:            3 * time.Second,
	}
	pool, err := pgkit.NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	checks := map[string]string{
		"statement_timeout":                   "7s",
		"idle_in_transaction_session_timeout": "11s",
		"lock_timeout":                        "3s",
	}
	for setting, want := range checks {
		var got string
		if err := pool.QueryRow(context.Background(), "SHOW "+setting).Scan(&got); err != nil {
			t.Fatalf("SHOW %s: %v", setting, err)
		}
		if got != want {
			t.Errorf("%s: want %q, got %q", setting, want, got)
		}
	}
}

// Сервер, принимающий TCP и молчащий: без ConnectTimeout NewPool висел бы вечно.
func TestNewPool_ConnectTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	cfg := pgkit.PoolConfig{
		DSN:            fmt.Sprintf("postgres://test:test@%s/test?sslmode=disable", ln.Addr()),
		MaxConns:       1,
		ConnectTimeout: 300 * time.Millisecond,
		HealthCheck:    time.Minute,
	}
	start := time.Now()
	_, err = pgkit.NewPool(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected connect error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("NewPool took %v, connect timeout not applied", elapsed)
	}
}

// Нулевой HealthCheck попадал в pgxpool как HealthCheckPeriod=0 и ронял процесс
// паникой time.NewTicker(0) из фоновой горутины пула.
func TestNewPool_ZeroHealthCheckDoesNotPanic(t *testing.T) {
	t.Parallel()
	_, err := pgkit.NewPool(context.Background(), pgkit.PoolConfig{
		DSN:            "postgres://test:test@127.0.0.1:1/test?sslmode=disable",
		MaxConns:       1,
		ConnectTimeout: 300 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected connect error on a dead port")
	}
}

func TestNewPool_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	_, err := pgkit.NewPool(context.Background(), pgkit.PoolConfig{})
	if !errors.Is(err, pgkit.ErrConfig) {
		t.Fatalf("NewPool(zero) err = %v, want ErrConfig", err)
	}
}

// Нули PoolConfig означают «дефолт pgxpool», а не «выключено».
func TestNewPool_ZeroSizesKeepPgxDefaults(t *testing.T) {
	pg := testkit.StartPostgres(t)
	pool, err := pgkit.NewPool(context.Background(), pgkit.PoolConfig{DSN: pg.DSN})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	cfg := pool.Config()
	if cfg.HealthCheckPeriod <= 0 {
		t.Errorf("HealthCheckPeriod = %v, want pgx default", cfg.HealthCheckPeriod)
	}
	if cfg.MaxConns < 1 {
		t.Errorf("MaxConns = %d, want pgx default >= 1", cfg.MaxConns)
	}
	if cfg.MaxConnLifetime <= 0 {
		t.Errorf("MaxConnLifetime = %v, want pgx default", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnIdleTime <= 0 {
		t.Errorf("MaxConnIdleTime = %v, want pgx default", cfg.MaxConnIdleTime)
	}
}

func TestNewPool_WithoutOTelTracer(t *testing.T) {
	pg := testkit.StartPostgres(t)
	cfg := pg.PoolConfig()
	cfg.OTelTrace = false
	pool, err := pgkit.NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	if tr := pool.Config().ConnConfig.Tracer; tr != nil {
		t.Fatalf("Tracer = %T, want nil when PG_OTEL_TRACE=false", tr)
	}
	if _, err := pool.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

// Регрессия KOBS-06: спан запроса otelpgx — `query <имя>` с db.system.name=postgresql: имя — операция
// или имя запроса sqlc из ведущего комментария (без него у каждого запроса sqlc имя — `--`), текст
// SQL (и литералы в нём) — только в db.query.text.
func TestNewPool_QuerySpanNameIsQueryName(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	pg := testkit.StartPostgres(t)
	pool, err := pgkit.NewPool(context.Background(), pg.PoolConfig())
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	// otelpgx пишет спан запроса только внутри записываемого спана вызова.
	ctx, parent := otel.Tracer("test").Start(context.Background(), "request")
	queries := map[string]string{
		"select 'zzspan-literal'::text":                               "SELECT",
		"-- name: CountLiterals :one\nselect 'zzspan-literal'::bytea": "CountLiterals",
	}
	for query := range queries {
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	parent.End()

	found := 0
	for _, s := range rec.Ended() {
		if strings.Contains(s.Name(), "zzspan-literal") {
			t.Errorf("span name %q carries the SQL text", s.Name())
		}
		attrs := map[string]string{}
		for _, a := range s.Attributes() {
			attrs[string(a.Key)] = a.Value.String()
		}
		name, ok := queries[attrs["db.query.text"]]
		if !ok {
			continue
		}
		found++
		if s.Name() != "query "+name || attrs["db.system.name"] != "postgresql" || attrs["db.operation.name"] != name {
			t.Errorf("span %q attrs %v, want query %s with db.system.name=postgresql and db.operation.name=%s", s.Name(), attrs, name, name)
		}
	}
	if found != len(queries) {
		t.Fatalf("%d otelpgx spans with db.query.text of the queries, want %d", found, len(queries))
	}
}

// Имя запроса — из конечного множества: имя sqlc или первое слово SQL после ведущих комментариев,
// а не `--`, `/*` или обрывок текста.
func TestQueryName(t *testing.T) {
	for sql, want := range map[string]string{
		"SELECT 1":      "SELECT",
		"select*from t": "SELECT",
		"-- name: CountUsers :one\nSELECT COUNT(*) FROM users":      "CountUsers",
		"/* name: GetUser :one */ SELECT 1":                         "GetUser",
		"-- get one\nSELECT 2":                                      "SELECT",
		"/* get one */ update t set a = 1":                          "UPDATE",
		" \n\t-- a\n/* b */\n with x as (select 1) select * from x": "WITH",
		"(select 1) union (select 2)":                               "SELECT",
		"-- name:\ninsert into t values (1)":                        "INSERT",
		"":                                                          "UNKNOWN",
		"-- only a comment":                                         "UNKNOWN",
		"/* not closed":                                             "UNKNOWN",
	} {
		if got := pgkit.QueryName(sql); got != want {
			t.Errorf("QueryName(%q) = %q, want %q", sql, got, want)
		}
	}
}
