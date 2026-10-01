package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

// durationCounts — число наблюдений гистограммы запросов по status (и mode).
func durationCounts(d metrictest.Snapshot) map[string]uint64 {
	got := map[string]uint64{}
	for _, s := range d.Find("http_client_request_duration_seconds") {
		key := s.Labels["status"]
		if mode, ok := s.Labels["mode"]; ok {
			key += "/" + mode
		}
		got[key] += s.Count
	}
	return got
}

func TestMetrics_RequestDurationLabels(t *testing.T) {
	before := metrictest.Collect(t)

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

	d := metrictest.Collect(t).Sub(before)
	got := durationCounts(d)
	for _, want := range []string{"200", "503", "breaker_open", "200/sse"} {
		if got[want] == 0 {
			t.Errorf("no data point with status=%q (all: %v)", want, got)
		}
	}
	if n := d.Value("http_client_retry_attempts_total"); n != 1 {
		t.Errorf("http_client_retry_attempts_total: got %v, want 1", n)
	}
	if n := d.Value("http_client_breaker_transitions_total"); n == 0 {
		t.Error("http_client_breaker_transitions_total: no transitions recorded")
	}
	if n := d.Value("http_client_breaker_open"); n != 1 {
		t.Errorf("http_client_breaker_open: got %v, want 1 (open)", n)
	}
}

func TestMetrics_SSEDialErrorRecorded(t *testing.T) {
	before := metrictest.Collect(t)

	c := newTestClient(t, Config{}, WithTransport(rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial refused")
	})))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://upstream.invalid/stream", nil)
	if _, err := c.Stream(req); err == nil {
		t.Fatal("expected dial error")
	}
	if got := durationCounts(metrictest.Collect(t).Sub(before)); got["error/sse"] == 0 {
		t.Errorf("sse dial error must be recorded as status=error, mode=sse (all: %v)", got)
	}
}
