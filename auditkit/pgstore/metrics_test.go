package pgstore_test

import (
	"context"
	"testing"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func TestStore_Metrics(t *testing.T) {
	before := metrictest.Collect(t)
	s, _, ctx := startStore(t)

	if err := s.Log(ctx, auditkit.Entry{Action: "metric.ok", Resource: "user"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if err := s.Log(ctx, auditkit.Entry{Resource: "user"}); err == nil {
		t.Fatal("Log with empty Action must fail")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Log(canceled, auditkit.Entry{Action: "metric.err", Resource: "user"}); err == nil {
		t.Fatal("Log with canceled ctx must fail")
	}

	d := metrictest.Collect(t).Sub(before)
	for _, result := range []string{"ok", "invalid", "error"} {
		if got := d.Get(t, "audit_log_writes_total", "result", result); got.Kind != metrictest.Counter || got.Value != 1 {
			t.Errorf("audit_log_writes_total: %v (%s), want counter delta 1", got, got.Kind)
		}
	}
	// Невалидная запись до INSERT не доходит: длительность — только у ok и error.
	for _, result := range []string{"ok", "error"} {
		dur := d.Get(t, "audit_log_write_duration_seconds", "result", result)
		if dur.Kind != metrictest.Histogram || dur.Unit != "s" || dur.Count != 1 || len(dur.Bounds) == 0 {
			t.Errorf("audit_log_write_duration_seconds: %v (%s, unit %q, bounds %v), want histogram in s with explicit buckets", dur, dur.Kind, dur.Unit, dur.Bounds)
		}
	}
}
