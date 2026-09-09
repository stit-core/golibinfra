package otelkit

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestLongDurationBuckets(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	h := Must(mp.Meter("test").Float64Histogram("long_seconds", metric.WithUnit("s"), LongDurationBuckets()))
	h.Record(context.Background(), 90)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	hist, ok := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("data: got %T, want Histogram[float64]", rm.ScopeMetrics[0].Metrics[0].Data)
	}
	want := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}
	got := hist.DataPoints[0].Bounds
	if len(got) != len(want) {
		t.Fatalf("bounds: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bounds[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
	// Хвост LLM-вызова обязан попадать в конечный bucket, а не в +Inf.
	if n := hist.DataPoints[0].BucketCounts[13]; n != 1 {
		t.Errorf("90s landed outside the (60, 120] bucket: counts = %v", hist.DataPoints[0].BucketCounts)
	}
}
