package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, cfg Config, opts ...Option) *Client {
	t.Helper()
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.RetryWait == 0 {
		cfg.RetryWait = 5 * time.Millisecond
	}
	c, err := New(cfg, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestClient_200NoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 3})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Errorf("expected 1 call, got %d", calls.Load())
	}
}

func TestClient_5xxRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 3})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 calls, got %d", calls.Load())
	}
}

func TestClient_5xxExhaustedRetries_ReturnsLastResponse(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream down")
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 2})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("5xx must be returned as response, got error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "upstream down" {
		t.Fatalf("body of last response must be readable, got %q", body)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 calls, got %d", calls.Load())
	}
}

func TestClient_PostWithBody_NotRetriedByDefault(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 3})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(`{"a":1}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Errorf("POST must not be retried by default, got %d calls", calls.Load())
	}
}

func TestClient_PostWithBody_RetriedWhenAllowed_BodyPreserved(t *testing.T) {
	var (
		calls  atomic.Int32
		bodies []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 3})
	ctx := AllowRetry(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(`{"a":1}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	if len(bodies) != 3 {
		t.Fatalf("calls: got %d", len(bodies))
	}
	for i, b := range bodies {
		if b != `{"a":1}` {
			t.Errorf("attempt %d: body %q, want full body on every attempt", i, b)
		}
	}
}

func TestClient_IdempotencyKeyHeader_AllowsRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 2})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader("x"))
	req.Header.Set("Idempotency-Key", "k1")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls.Load() != 2 {
		t.Errorf("expected retry with Idempotency-Key, got %d calls", calls.Load())
	}
}

func TestClient_UnrewindableBody_NotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 3, RetryNonIdempotent: true})
	pr, pw := io.Pipe()
	go func() { _, _ = pw.Write([]byte("stream")); pw.Close() }()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, srv.URL, pr)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Errorf("body without GetBody must not be retried, got %d calls", calls.Load())
	}
}

func TestClient_RetryAfterHonored(t *testing.T) {
	var (
		calls atomic.Int32
		first time.Time
		gap   time.Duration
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			first = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			gap = time.Since(first)
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 1, RetryWait: time.Millisecond, RetryMaxWait: 5 * time.Second})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if gap < 900*time.Millisecond {
		t.Errorf("Retry-After: waited only %v", gap)
	}
}

func TestClient_BeforeAttemptCalledPerAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var attempts []int
	c := newTestClient(t, Config{MaxRetries: 3}, WithBeforeAttempt(func(_ context.Context, attempt int) error {
		attempts = append(attempts, attempt)
		return nil
	}))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if len(attempts) != 3 || attempts[0] != 0 || attempts[2] != 2 {
		t.Errorf("before-attempt calls: %v", attempts)
	}
}

func TestClient_BeforeAttemptErrorAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	limited := errors.New("rate limited")
	c := newTestClient(t, Config{MaxRetries: 0}, WithBeforeAttempt(func(context.Context, int) error { return limited }))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err := c.Do(req)
	if !errors.Is(err, limited) {
		t.Fatalf("want before-attempt error, got %v", err)
	}
}

func TestClient_RequestIDForward(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-Request-ID"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 0})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	req.Header.Set("X-Request-ID", "explicit-id")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	ctx := contextWithRequestID(context.Background(), "from-ctx")
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if got[0] != "explicit-id" || got[1] != "from-ctx" {
		t.Errorf("X-Request-ID: got %v", got)
	}
}

func TestClient_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{Timeout: 50 * time.Millisecond, MaxRetries: 0})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err := c.Do(req)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := map[string]Config{
		"retry without wait":       {MaxRetries: 3, RetryWait: 0},
		"negative retries":         {MaxRetries: -1, RetryWait: time.Second},
		"max wait below base":      {MaxRetries: 1, RetryWait: time.Second, RetryMaxWait: time.Millisecond},
		"breaker without recovery": {Breaker: BreakerConfig{FailureThreshold: 1}},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: want ErrConfig, got %v", name, err)
		}
	}
	if _, err := New(Config{}); err != nil {
		t.Errorf("zero config (no retries, no breaker) must be valid: %v", err)
	}
}

func TestBackoff_CappedAndJittered(t *testing.T) {
	c := newTestClient(t, Config{MaxRetries: 5, RetryWait: 100 * time.Millisecond, RetryMaxWait: 250 * time.Millisecond})
	for attempt := 1; attempt <= 40; attempt++ {
		d := c.backoff(attempt, nil)
		if d < 100*time.Millisecond || d > 250*time.Millisecond {
			t.Fatalf("attempt %d: backoff %v out of [100ms, 250ms]", attempt, d)
		}
	}
}
