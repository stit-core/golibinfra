package cachekit

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMetricsHook_RecordsResult(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	hook := newMetricsHookWithMeter(provider.Meter("test"))

	ctx := context.Background()
	results := []error{nil, redis.Nil, errors.New("boom")}
	for _, res := range results {
		process := hook.ProcessHook(func(context.Context, redis.Cmder) error { return res })
		_ = process(ctx, redis.NewStringCmd(ctx, "get", "k"))
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "cache_commands_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("unexpected data type %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				v, _ := dp.Attributes.Value("result")
				seen[v.AsString()] += dp.Value
			}
		}
	}
	for _, want := range []string{"ok", "miss", "error"} {
		if seen[want] != 1 {
			t.Errorf("result=%s: got %d, want 1 (all: %v)", want, seen[want], seen)
		}
	}
}
