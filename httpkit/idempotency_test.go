package httpkit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIdempotency_FirstCallHandlerInvoked(t *testing.T) {
	store := NewMemoryStore(time.Minute)
	cfg := IdempotencyConfig{Header: "Idempotency-Key", TTL: time.Minute, MaxBodyBytes: 65536}

	calls := 0
	handler := Idempotency(cfg, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Idempotency-Key", "key-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if calls != 1 {
		t.Fatalf("expected handler called once, got %d", calls)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
}

func TestIdempotency_SecondCallFromCache(t *testing.T) {
	store := NewMemoryStore(time.Minute)
	cfg := IdempotencyConfig{Header: "Idempotency-Key", TTL: time.Minute, MaxBodyBytes: 65536}

	calls := 0
	handler := Idempotency(cfg, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
		req.Header.Set("Idempotency-Key", "key-dup")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	if calls != 1 {
		t.Fatalf("expected handler called once, got %d", calls)
	}
}

func TestIdempotency_GetSkipped(t *testing.T) {
	store := NewMemoryStore(time.Minute)
	cfg := IdempotencyConfig{Header: "Idempotency-Key", TTL: time.Minute, MaxBodyBytes: 65536}

	calls := 0
	handler := Idempotency(cfg, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Idempotency-Key", "key-get")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	if calls != 3 {
		t.Fatalf("expected handler called 3 times for GET, got %d", calls)
	}
}
