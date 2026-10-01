package anthropickit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// Латентность генерации меряется минутами: на общей шкале китов (потолок 30 с)
// весь хвост лёг бы в +Inf и алерт по p99 стал бы бессмысленным.
func TestMetrics_LatencyBucketsCoverGenerationScale(t *testing.T) {
	metrics, _ := installOTel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	if _, err := c.Complete(context.Background(), userRequest("hi")); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got := metrics().Find("anthropic_api_latency_seconds")
	if len(got) == 0 {
		t.Fatal("anthropic_api_latency_seconds is missing")
	}
	want := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}
	if !slices.Equal(got[0].Bounds, want) {
		t.Fatalf("bounds = %v, want %v", got[0].Bounds, want)
	}
}
