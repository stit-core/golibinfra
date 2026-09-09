package pgkit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
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

	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	regPrimary := pgkit.InstrumentPool(pool, pgkit.RolePrimary)
	defer regPrimary.Unregister()
	regReplica := pgkit.InstrumentPool(pool, pgkit.RoleReplica)
	defer regReplica.Unregister()

	if _, err := pool.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

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
	roles := map[string]map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, role := range dataPointRoles(m) {
				if roles[m.Name] == nil {
					roles[m.Name] = map[string]bool{}
				}
				roles[m.Name][role] = true
			}
		}
	}
	for _, w := range want {
		if !roles[w][pgkit.RolePrimary] || !roles[w][pgkit.RoleReplica] {
			names := make([]string, 0, len(roles))
			for n := range roles {
				names = append(names, n)
			}
			t.Fatalf("metric %s lacks role series (%v); got: %s", w, roles[w], strings.Join(names, ", "))
		}
	}
}

func dataPointRoles(m metricdata.Metrics) []string {
	var out []string
	add := func(set attribute.Set) {
		if v, ok := set.Value("role"); ok {
			out = append(out, v.AsString())
		}
	}
	switch d := m.Data.(type) {
	case metricdata.Gauge[int64]:
		for _, p := range d.DataPoints {
			add(p.Attributes)
		}
	case metricdata.Sum[int64]:
		for _, p := range d.DataPoints {
			add(p.Attributes)
		}
	case metricdata.Sum[float64]:
		for _, p := range d.DataPoints {
			add(p.Attributes)
		}
	}
	return out
}
