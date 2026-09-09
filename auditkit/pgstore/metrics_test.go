package pgstore_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.stit.tech/stit-core/golibinfra/auditkit"
)

func TestStore_Metrics(t *testing.T) {
	s, _, ctx := startStore(t)

	before := writeCounts(t)

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

	after := writeCounts(t)
	for _, result := range []string{"ok", "invalid", "error"} {
		if got := after[result] - before[result]; got != 1 {
			t.Fatalf("audit_log_writes_total{result=%s} delta = %d, want 1 (before %v, after %v)", result, got, before, after)
		}
	}
	if !hasDurationBuckets(t, "audit_log_write_duration_seconds") {
		t.Fatal("audit_log_write_duration_seconds not emitted with explicit buckets")
	}
}

func collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

func writeCounts(t *testing.T) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, sm := range collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "audit_log_writes_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("audit_log_writes_total: unexpected data type %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				result, _ := dp.Attributes.Value("result")
				out[result.AsString()] += dp.Value
			}
		}
	}
	return out
}

func hasDurationBuckets(t *testing.T, name string) bool {
	t.Helper()
	for _, sm := range collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s: unexpected data type %T", name, m.Data)
			}
			return len(h.DataPoints) > 0 && len(h.DataPoints[0].Bounds) > 0
		}
	}
	return false
}
