package anthropickit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Латентность генерации меряется минутами: на общей шкале китов (потолок 30 с)
// весь хвост лёг бы в +Inf и алерт по p99 стал бы бессмысленным.
func TestMetrics_LatencyBucketsCoverGenerationScale(t *testing.T) {
	reader, _ := installOTel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	if _, err := c.Complete(context.Background(), userRequest("hi")); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	bounds := latencyBounds(t, reader)
	want := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}
	if len(bounds) != len(want) {
		t.Fatalf("bounds = %v, want %v", bounds, want)
	}
	for i := range want {
		if bounds[i] != want[i] {
			t.Fatalf("bounds[%d] = %v, want %v", i, bounds[i], want[i])
		}
	}
}

func latencyBounds(t *testing.T, reader *sdkmetric.ManualReader) []float64 {
	t.Helper()
	for _, sm := range collect(t, reader).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "anthropic_api_latency_seconds" {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("anthropic_api_latency_seconds is %T, want Histogram[float64]", m.Data)
			}
			if len(h.DataPoints) == 0 {
				t.Fatal("anthropic_api_latency_seconds has no datapoints")
			}
			if h.DataPoints[0].Sum < 0 {
				t.Fatalf("latency sum = %v", h.DataPoints[0].Sum)
			}
			return h.DataPoints[0].Bounds
		}
	}
	t.Fatal("anthropic_api_latency_seconds is missing")
	return nil
}
