package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// metricReader ставит глобальный MeterProvider на время теста: инструменты
// китов берут его в момент New.
func metricReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	return reader
}

// durationLabels собирает значения label'а status (и mode) гистограммы запросов.
func durationLabels(t *testing.T, reader *sdkmetric.ManualReader) map[string]uint64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := map[string]uint64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http_client_request_duration_seconds" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("unexpected data type %T", m.Data)
			}
			for _, dp := range hist.DataPoints {
				status, _ := dp.Attributes.Value("status")
				key := status.AsString()
				if mode, ok := dp.Attributes.Value("mode"); ok {
					key += "/" + mode.AsString()
				}
				got[key] += dp.Count
			}
		}
	}
	return got
}

func counterValue(t *testing.T, reader *sdkmetric.ManualReader, name string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s: unexpected data type %T", name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
		}
	}
	return total
}

func TestMetrics_RequestDurationLabels(t *testing.T) {
	reader := metricReader(t)

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ok.Close)
	sse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: x\n\n")
	}))
	t.Cleanup(sse.Close)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)

	c := newTestClient(t, Config{MaxRetries: 1, Breaker: BreakerConfig{FailureThreshold: 1, RecoveryTimeout: time.Minute}})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ok.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("ok request: %v", err)
	}
	resp.Body.Close()

	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, down.URL, nil)
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
	}
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, down.URL, nil)
	if _, err := c.Do(req); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("want ErrBreakerOpen, got %v", err)
	}

	stream := openStream(t, c, sse.URL)
	collect(stream)

	got := durationLabels(t, reader)
	for _, want := range []string{"200", "503", "breaker_open", "200/sse"} {
		if got[want] == 0 {
			t.Errorf("no data point with status=%q (all: %v)", want, got)
		}
	}
	if n := counterValue(t, reader, "http_client_retry_attempts_total"); n != 1 {
		t.Errorf("http_client_retry_attempts_total: got %d, want 1", n)
	}
	if n := counterValue(t, reader, "http_client_breaker_transitions_total"); n == 0 {
		t.Error("http_client_breaker_transitions_total: no transitions recorded")
	}
	if n := counterValue(t, reader, "http_client_breaker_open"); n != 1 {
		t.Errorf("http_client_breaker_open: got %d, want 1 (open)", n)
	}
}

func TestMetrics_SSEDialErrorRecorded(t *testing.T) {
	reader := metricReader(t)

	c := newTestClient(t, Config{}, WithTransport(rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial refused")
	})))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://upstream.invalid/stream", nil)
	if _, err := c.Stream(req); err == nil {
		t.Fatal("expected dial error")
	}
	if got := durationLabels(t, reader); got["error/sse"] == 0 {
		t.Errorf("sse dial error must be recorded as status=error, mode=sse (all: %v)", got)
	}
}
