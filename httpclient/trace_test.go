package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
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
				if a.Key == "url.full" && a.Value.AsString() != srv.URL+"/items?token=xxxxx" {
					t.Errorf("url.full must mask the query values, got %q", a.Value.AsString())
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

// Регрессия KSEC-03 (E03, E04): секреты URL не уходят ни в спаны вызова и попытки, ни в ошибку
// вызывающему; спан попытки несёт свой trace-context в upstream.
func TestTrace_NoURLSecretsInSpansAndErrors(t *testing.T) {
	rec := spanRecorder(t)
	prevProp := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prevProp) })

	var traceparent atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent.Store(r.Header.Get("Traceparent"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, Config{})

	ok := strings.Replace(srv.URL, "http://", "http://bob:s3cret@", 1) + "/ok?access_token=s3cret"
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ok, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	down, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/down?access_token=s3cret", nil)
	_, err = c.Do(down)
	var urlErr *url.Error
	if err == nil || strings.Contains(err.Error(), "s3cret") || !errors.As(err, &urlErr) {
		t.Fatalf("Do error = %v, want a *url.Error without the token", err)
	}
	if down.Header.Get("Traceparent") != "" {
		t.Error("trace-context was written into the caller's request")
	}

	var attemptID string
	for _, s := range rec.Ended() {
		var dump strings.Builder
		dump.WriteString(s.Name() + " " + s.Status().Description)
		for _, a := range s.Attributes() {
			dump.WriteString(" " + string(a.Key) + "=" + a.Value.String())
		}
		for _, ev := range s.Events() {
			for _, a := range ev.Attributes {
				dump.WriteString(" " + a.Value.String())
			}
		}
		if strings.Contains(dump.String(), "s3cret") {
			t.Errorf("span keeps a secret: %s", dump.String())
		}
		if s.Name() == "HTTP GET" && attemptID == "" {
			attemptID = s.SpanContext().SpanID().String()
		}
	}
	if tp, _ := traceparent.Load().(string); attemptID == "" || !strings.Contains(tp, attemptID) {
		t.Errorf("upstream traceparent %q, want the attempt span %s as parent", tp, attemptID)
	}
}
