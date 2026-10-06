package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Регрессия KSEC-06: label host — у первых MaxHosts хостов, остальные — "other" в длительности
// Do и Stream и в повторах: адреса пользователей не растят число серий.
func TestMetrics_HostOverMaxHostsIsOther(t *testing.T) {
	before := metrictest.Collect(t)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(first.Close)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	sse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: x\n\n")
	}))
	t.Cleanup(sse.Close)

	c := newTestClient(t, Config{MaxHosts: 1, MaxRetries: 1})
	for _, url := range []string{first.URL, down.URL} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("Do %s: %v", url, err)
		}
		resp.Body.Close()
	}
	collect(openStream(t, c, sse.URL))

	d := metrictest.Collect(t).Sub(before)
	firstHost := strings.TrimPrefix(first.URL, "http://")
	d.Get(t, "http_client_request_duration_seconds", "host", firstHost, "method", "GET", "status", "200")
	d.Get(t, "http_client_request_duration_seconds", "host", "other", "method", "GET", "status", "503")
	d.Get(t, "http_client_request_duration_seconds", "host", "other", "method", "GET", "status", "200", "mode", "sse")
	if n := d.Value("http_client_retry_attempts_total", "host", "other"); n != 1 {
		t.Errorf("http_client_retry_attempts_total{host=other}: %v, want 1", n)
	}
	for _, s := range d.All() {
		if h, ok := s.Labels["host"]; ok && h != firstHost && h != "other" {
			t.Errorf("series with a host over MaxHosts: %s", s)
		}
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

// Метрики otelhttp клиент не эмитит: http.client.request.duration в Prometheus получил бы имя
// гистограммы кита, и серии двух инструментов слились бы. Спаны попыток остаются (trace_test).
func TestMetrics_NoOtelhttpMetricsNoNameCollisions(t *testing.T) {
	before := metrictest.Collect(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(`{"a":1}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	snap := metrictest.Collect(t)
	for _, s := range snap.Sub(before).All() {
		if !strings.HasPrefix(s.Name, "http_client_") {
			t.Errorf("metric outside the kit contract: %s (scope %s)", s, s.Scope)
		}
	}
	if len(snap.Sub(before).Find("http_client_request_duration_seconds")) == 0 {
		t.Fatal("kit histogram http_client_request_duration_seconds not recorded")
	}
	if got := snap.Collisions(); len(got) != 0 {
		t.Errorf("Prometheus name collisions: %v", got)
	}
}
