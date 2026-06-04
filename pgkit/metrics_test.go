package pgkit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.sftbmb.ru/sftbmb-core/golibinfra/pgkit"
	"git.sftbmb.ru/sftbmb-core/golibinfra/testkit"
)

func TestInstrumentPool_EmitsPoolStats(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pg := testkit.StartPostgres(t, "")
	cfg := pgkit.PoolConfig{
		DSN: pg.DSN, MaxConns: 5, MinConns: 1,
		MaxConnLifetime: 5 * time.Minute, MaxConnIdleTime: time.Minute,
		HealthCheck:      30 * time.Second,
		StatementTimeout: 7 * time.Second,
	}
	pool, err := pgkit.NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := mp.Meter("pgkit-test")

	reg, err := pgkit.InstrumentPool(pool, meter)
	if err != nil {
		t.Fatalf("InstrumentPool: %v", err)
	}
	defer reg.Unregister()

	// Trigger one acquire to bump counters.
	if _, err := pool.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	want := []string{
		"db_pool_acquired_conns",
		"db_pool_idle_conns",
		"db_pool_total_conns",
		"db_pool_max_conns",
		"db_pool_acquire_total",
		"db_pool_acquire_duration_seconds_total",
	}
	got := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			got[m.Name] = true
		}
	}
	for _, w := range want {
		if !got[w] {
			names := make([]string, 0, len(got))
			for n := range got {
				names = append(names, n)
			}
			t.Fatalf("missing metric %s; got: %s", w, strings.Join(names, ", "))
		}
	}
}
