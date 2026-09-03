package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
		w.WriteHeader(http.StatusInternalServerError)
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
	probeHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if probeHit {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
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

	time.Sleep(recovery + 10*time.Millisecond)
	probeHit = true

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
