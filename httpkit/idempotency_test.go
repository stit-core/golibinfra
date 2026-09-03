package httpkit

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

func idemCfg() IdempotencyConfig {
	return IdempotencyConfig{Header: "Idempotency-Key", TTL: time.Minute, ReserveTTL: time.Minute, MaxBodyBytes: 64}
}

func postWithKey(key, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Authorization", "Bearer alice")
	return req
}

func createdHandler(calls *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/orders/1")
		w.Header().Set("X-Custom", "not-replayed")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
}

func TestIdempotency_ReplaysSameRequest(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore())(createdHandler(&calls))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, postWithKey("k1", `{"a":1}`))
	second := httptest.NewRecorder()
	h.ServeHTTP(second, postWithKey("k1", `{"a":1}`))

	if calls.Load() != 1 {
		t.Fatalf("handler calls: %d", calls.Load())
	}
	if second.Code != http.StatusCreated || second.Body.String() != `{"id":1}` {
		t.Fatalf("replay: %d %q", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("replay must be marked")
	}
	if second.Header().Get("Location") != "/orders/1" || second.Header().Get("Content-Type") != "application/json" {
		t.Errorf("replay headers: %v", second.Header())
	}
	if second.Header().Get("X-Custom") != "" {
		t.Error("non-whitelisted header must not be replayed")
	}
}

func TestIdempotency_DifferentBodySameKey_Conflict(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore())(createdHandler(&calls))

	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{"a":1}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postWithKey("k1", `{"a":2}`))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler must not run for mismatched fingerprint, calls=%d", calls.Load())
	}
}

func TestIdempotency_KeyScopedToClient(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore())(createdHandler(&calls))

	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{"a":1}`))
	other := postWithKey("k1", `{"a":1}`)
	other.Header.Set("Authorization", "Bearer bob")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, other)

	if calls.Load() != 2 {
		t.Fatalf("another client with the same key must execute its own request, calls=%d", calls.Load())
	}
	if rec.Header().Get("Idempotent-Replayed") != "" {
		t.Error("must not replay another client's response")
	}
}

func TestIdempotency_GetSkipped(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore())(createdHandler(&calls))
	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "/orders", nil)
		req.Header.Set("Idempotency-Key", "k-get")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls.Load() != 3 {
		t.Fatalf("GET must bypass idempotency, calls=%d", calls.Load())
	}
}

func TestIdempotency_5xxNotCached(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{}`))
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{}`))
	if calls.Load() != 2 {
		t.Fatalf("5xx must not be replayed, calls=%d", calls.Load())
	}
}

func TestIdempotency_OversizedBodySkipped(t *testing.T) {
	var (
		calls atomic.Int32
		seen  int
	)
	h := Idempotency(idemCfg(), NewMemoryStore())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		seen = len(b)
		w.WriteHeader(http.StatusCreated)
	}))
	big := strings.Repeat("x", 200)
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", big))
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", big))
	if calls.Load() != 2 {
		t.Fatalf("oversized body must pass through without idempotency, calls=%d", calls.Load())
	}
	if seen != 200 {
		t.Fatalf("handler must see the whole body, got %d bytes", seen)
	}
}

type stubStore struct {
	reserve bool
	err     error
}

func (s stubStore) Reserve(context.Context, string, time.Duration) (bool, error) {
	return s.reserve, s.err
}
func (s stubStore) Get(context.Context, string) ([]byte, bool, error)        { return nil, false, s.err }
func (s stubStore) Set(context.Context, string, []byte, time.Duration) error { return s.err }
func (s stubStore) Release(context.Context, string) error                    { return nil }

func TestIdempotency_InProgress(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), stubStore{reserve: false})(createdHandler(&calls))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postWithKey("k1", `{}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 while in progress, got %d", rec.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("handler must not run while the key is reserved")
	}
}

func TestIdempotency_StoreErrorFailsOpen(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), stubStore{err: errors.New("redis down")})(createdHandler(&calls))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postWithKey("k1", `{}`))
	if rec.Code != http.StatusCreated || calls.Load() != 1 {
		t.Fatalf("store error must fail open: %d calls=%d", rec.Code, calls.Load())
	}
}

func TestMemoryStore_Expiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := NewMemoryStore()
	s.now = func() time.Time { return now }
	ctx := context.Background()

	if ok, _ := s.Reserve(ctx, "k", time.Minute); !ok {
		t.Fatal("first reserve")
	}
	if ok, _ := s.Reserve(ctx, "k", time.Minute); ok {
		t.Fatal("reserve while reserved")
	}
	now = now.Add(2 * time.Minute)
	if ok, _ := s.Reserve(ctx, "k", time.Minute); !ok {
		t.Fatal("reserve after expiry")
	}
	_ = s.Set(ctx, "k", []byte("rec"), time.Minute)
	if v, found, _ := s.Get(ctx, "k"); !found || string(v) != "rec" {
		t.Fatalf("get: %q %v", v, found)
	}
	now = now.Add(2 * time.Minute)
	if _, found, _ := s.Get(ctx, "k"); found {
		t.Fatal("record must expire")
	}
}
