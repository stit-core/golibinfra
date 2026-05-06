package httpclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
)

func TestCircuitBreaker_OpensAfterFailures(t *testing.T) {
	threshold := 5
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := Config{
		Timeout:    5 * time.Second,
		MaxRetries: 0,
		RetryWait:  0,
		Breaker: BreakerConfig{
			FailureThreshold: threshold,
			RecoveryTimeout:  30 * time.Second,
			SuccessThreshold: 2,
		},
	}
	client := New(cfg)

	for i := 0; i < threshold; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		_, _ = client.Do(req)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error from open circuit breaker")
	}
	if err != gobreaker.ErrOpenState {
		t.Fatalf("expected ErrOpenState, got: %v", err)
	}
}

func TestCircuitBreaker_RecoveryTimeout(t *testing.T) {
	threshold := 2
	probeHit := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if probeHit {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	recoveryTimeout := 50 * time.Millisecond
	cfg := Config{
		Timeout:    5 * time.Second,
		MaxRetries: 0,
		RetryWait:  0,
		Breaker: BreakerConfig{
			FailureThreshold: threshold,
			RecoveryTimeout:  recoveryTimeout,
			SuccessThreshold: 1,
		},
	}
	client := New(cfg)

	for i := 0; i < threshold; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		_, _ = client.Do(req)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := client.Do(req)
	if err != gobreaker.ErrOpenState {
		t.Fatalf("expected open state, got: %v", err)
	}

	time.Sleep(recoveryTimeout + 10*time.Millisecond)
	probeHit = true

	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("expected probe to succeed, got: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	_ = fmt.Sprintf("probe succeeded")
}
