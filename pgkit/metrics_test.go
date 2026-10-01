package pgkit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func TestInstrumentPool_EmitsPoolStatsPerRole(t *testing.T) {
	pg := testkit.StartPostgres(t)
	cfg := pgkit.PoolConfig{
		DSN: pg.DSN, MaxConns: 5, MinConns: 1,
		MaxConnLifetime: 5 * time.Minute, MaxConnIdleTime: time.Minute,
		HealthCheck: 30 * time.Second, ConnectTimeout: 10 * time.Second,
	}
	pool, err := pgkit.NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	regPrimary := pgkit.InstrumentPool(pool, pgkit.RolePrimary)
	defer regPrimary.Unregister()
	regReplica := pgkit.InstrumentPool(pool, pgkit.RoleReplica)
	defer regReplica.Unregister()

	if _, err := pool.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}

	snap := metrictest.Collect(t)

	want := []string{
		"pg_pool_acquired_conns",
		"pg_pool_idle_conns",
		"pg_pool_total_conns",
		"pg_pool_max_conns",
		"pg_pool_constructing_conns",
		"pg_pool_empty_acquire_total",
		"pg_pool_canceled_acquire_total",
		"pg_pool_acquire_total",
		"pg_pool_acquire_duration_seconds_total",
	}
	for _, w := range want {
		for _, role := range []string{pgkit.RolePrimary, pgkit.RoleReplica} {
			if len(snap.Find(w, "role", role)) == 0 {
				t.Fatalf("metric %s lacks role=%s series; got: %v", w, role, snap.Find(w))
			}
		}
	}
}
