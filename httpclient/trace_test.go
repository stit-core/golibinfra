package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func spanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func TestTrace_CallSpanAndResendCount(t *testing.T) {
	rec := spanRecorder(t)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 1, Breaker: BreakerConfig{FailureThreshold: 0}})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/items?token=secret", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	var (
		call     bool
		resends  []int64
		attempts int
	)
	for _, s := range rec.Ended() {
		if s.Name() == "httpclient.Do GET" {
			call = true
			for _, a := range s.Attributes() {
				if a.Key == "url.full" && a.Value.AsString() != srv.URL+"/items" {
					t.Errorf("url.full must drop the query, got %q", a.Value.AsString())
				}
			}
			continue
		}
		attempts++
		for _, a := range s.Attributes() {
			if a.Key == "http.request.resend_count" {
				resends = append(resends, a.Value.AsInt64())
			}
		}
	}
	if !call {
		t.Fatalf("no httpclient.Do span; got %d spans", len(rec.Ended()))
	}
	if attempts != 2 {
		t.Errorf("attempt spans: got %d, want 2", attempts)
	}
	if len(resends) != 1 || resends[0] != 1 {
		t.Errorf("http.request.resend_count on retries: got %v, want [1]", resends)
	}
}
