package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/internal/requestid"
)

func contextWithRequestID(ctx context.Context, id string) context.Context {
	return requestid.With(ctx, id)
}

func failingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCircuitBreaker_OpensAfterFailures(t *testing.T) {
	srv := failingServer(t)
	c := newTestClient(t, Config{Breaker: BreakerConfig{FailureThreshold: 5, RecoveryTimeout: 30 * time.Second}})

	for range 5 {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
		}
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := c.Do(req)
	if !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("expected ErrBreakerOpen, got: %v", err)
	}
}

func TestCircuitBreaker_500IsNotAFailure(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusInternalServerError)
	c := newTestClient(t, Config{Breaker: BreakerConfig{FailureThreshold: 2, RecoveryTimeout: 30 * time.Second}})

	for range 5 {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("500 from a live upstream must not trip the breaker: %v", err)
		}
		resp.Body.Close()
	}
}

func TestCircuitBreaker_PerHostIsolation(t *testing.T) {
	bad := failingServer(t)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(good.Close)

	c := newTestClient(t, Config{Breaker: BreakerConfig{FailureThreshold: 2, RecoveryTimeout: 30 * time.Second}})
	for range 2 {
		req, _ := http.NewRequest(http.MethodGet, bad.URL, nil)
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	req, _ := http.NewRequest(http.MethodGet, bad.URL, nil)
	if _, err := c.Do(req); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("bad host: want open breaker, got %v", err)
	}

	req, _ = http.NewRequest(http.MethodGet, good.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("good host must not be affected: %v", err)
	}
	resp.Body.Close()
}

func TestCircuitBreaker_RecoveryTimeout(t *testing.T) {
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	recovery := 50 * time.Millisecond
	c := newTestClient(t, Config{Breaker: BreakerConfig{FailureThreshold: 2, RecoveryTimeout: recovery}})

	for range 2 {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := c.Do(req); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("expected open state, got: %v", err)
	}

	// Проба в half-open проваливается — цепь снова Open.
	time.Sleep(recovery + 10*time.Millisecond)
	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := c.Do(req); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("failed half-open probe must re-open the breaker, got: %v", err)
	}

	time.Sleep(recovery + 10*time.Millisecond)
	healthy.Store(true)

	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("expected probe to succeed, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestCircuitBreaker_Disabled(t *testing.T) {
	srv := failingServer(t)
	c := newTestClient(t, Config{Breaker: BreakerConfig{FailureThreshold: 0}})
	for range 10 {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("breaker disabled: unexpected error %v", err)
		}
		resp.Body.Close()
	}
}

func TestCircuitBreaker_ContextCancelIsNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fast" {
			w.WriteHeader(http.StatusOK)
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, Config{MaxRetries: 0, Breaker: BreakerConfig{FailureThreshold: 2, RecoveryTimeout: time.Minute}})
	for range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/slow", nil)
		if _, err := c.Do(req); err == nil {
			t.Fatal("expected canceled request to fail")
		}
		cancel()
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/fast", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("caller cancellations must not open the breaker: %v", err)
	}
	resp.Body.Close()
}

func TestCircuitBreaker_BeforeAttemptErrorIsNotAFailure(t *testing.T) {
	var calls atomic.Int32
	srv := statusServer(t, &calls, http.StatusOK)

	limited := errors.New("rate limited")
	var reject atomic.Bool
	reject.Store(true)
	c := newTestClient(t, Config{MaxRetries: 0, Breaker: BreakerConfig{FailureThreshold: 2, RecoveryTimeout: time.Minute}},
		WithBeforeAttempt(func(context.Context, int) error {
			if reject.Load() {
				return limited
			}
			return nil
		}))

	for range 5 {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		if _, err := c.Do(req); !errors.Is(err, limited) {
			t.Fatalf("want limiter error, got %v", err)
		}
	}

	reject.Store(false)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("limiter rejections must not open the breaker: %v", err)
	}
	resp.Body.Close()
}
