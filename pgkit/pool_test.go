package pgkit_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

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
