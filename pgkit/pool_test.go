package pgkit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit/golibinfra/pgkit"
	"git.stit.tech/stit/golibinfra/testkit"
)

func TestNewPool_AppliesSessionTimeouts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pg := testkit.StartPostgres(t, "")
	cfg := pgkit.PoolConfig{
		DSN:                    pg.DSN,
		MaxConns:               2,
		MinConns:               1,
		MaxConnLifetime:        5 * time.Minute,
		MaxConnIdleTime:        time.Minute,
		HealthCheck:            30 * time.Second,
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
