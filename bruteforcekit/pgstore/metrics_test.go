package pgstore_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestStore_Metrics(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	cfg.MaxFailures = 2
	cfg.CleanupRetention = time.Hour
	s := newStore(t, db, cfg)

	const subject, ip = "metrics@example.com", "10.0.0.10"
	before := snapshot(t)

	if _, err := s.Check(ctx, subject, ip); err != nil {
		t.Fatalf("Check: %v", err)
	}
	for range cfg.MaxFailures {
		if _, err := s.RecordFailure(ctx, subject, ip); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	// Неудача поверх активной блокировки только продлевает её — не новый lock.
	if _, err := s.RecordFailure(ctx, subject, ip); err != nil {
		t.Fatalf("RecordFailure over lock: %v", err)
	}
	if _, err := s.Check(ctx, subject, ip); err != nil {
		t.Fatalf("Check locked: %v", err)
	}

	if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt)
		VALUES ('metrics-stale@example.com', '5.5.5.5', 1, NOW() - interval '2 hours')`); err != nil {
		t.Fatalf("insert stale: %v", err)
	}
	if err := s.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	after := snapshot(t)
	checks := map[string]int64{
		"bruteforce_checks_total{result=allowed}": after.checks["allowed"] - before.checks["allowed"],
		"bruteforce_checks_total{result=locked}":  after.checks["locked"] - before.checks["locked"],
		"bruteforce_locks_total":                  after.locks - before.locks,
		"bruteforce_cleanup_deleted_total":        after.cleanupDeleted - before.cleanupDeleted,
	}
	for name, want := range map[string]int64{
		"bruteforce_checks_total{result=allowed}": 1,
		"bruteforce_checks_total{result=locked}":  1,
		"bruteforce_locks_total":                  1,
		"bruteforce_cleanup_deleted_total":        1,
	} {
		if checks[name] != want {
			t.Fatalf("%s delta = %d, want %d", name, checks[name], want)
		}
	}
}

type metricsSnapshot struct {
	checks         map[string]int64
	locks          int64
	cleanupDeleted int64
}

func snapshot(t *testing.T) metricsSnapshot {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	s := metricsSnapshot{checks: map[string]int64{}}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				switch m.Name {
				case "bruteforce_checks_total":
					result, _ := dp.Attributes.Value("result")
					s.checks[result.AsString()] += dp.Value
				case "bruteforce_locks_total":
					s.locks += dp.Value
				case "bruteforce_cleanup_deleted_total":
					s.cleanupDeleted += dp.Value
				}
			}
		}
	}
	return s
}
