package httpkit

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"
)

type IdempotencyConfig struct {
	Header       string        `default:"Idempotency-Key"`
	TTL          time.Duration `default:"24h"`
	MaxBodyBytes int           `default:"65536"`
}

type IdempotencyStore interface {
	Get(ctx context.Context, key string) (response []byte, status int, found bool, err error)
	Set(ctx context.Context, key string, response []byte, status int) error
}

type memoryEntry struct {
	response  []byte
	status    int
	expiresAt time.Time
}

type MemoryStore struct {
	m   sync.Map
	ttl time.Duration
}

func NewMemoryStore(ttl time.Duration) *MemoryStore {
	return &MemoryStore{ttl: ttl}
}

func (s *MemoryStore) Get(_ context.Context, key string) ([]byte, int, bool, error) {
	v, ok := s.m.Load(key)
	if !ok {
		return nil, 0, false, nil
	}
	e := v.(memoryEntry)
	if time.Now().After(e.expiresAt) {
		s.m.Delete(key)
		return nil, 0, false, nil
	}
	return e.response, e.status, true, nil
}

func (s *MemoryStore) Set(_ context.Context, key string, response []byte, status int) error {
	s.m.Store(key, memoryEntry{
		response:  response,
		status:    status,
		expiresAt: time.Now().Add(s.ttl),
	})
	return nil
}

type idempotencyResponseWriter struct {
	http.ResponseWriter
	buf    bytes.Buffer
	status int
}

func (w *idempotencyResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *idempotencyResponseWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}

func Idempotency(cfg IdempotencyConfig, store IdempotencyStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
			default:
				next.ServeHTTP(w, r)
				return
			}

			key := r.Header.Get(cfg.Header)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			cached, status, found, err := store.Get(r.Context(), key)
			if err == nil && found {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(cached)
				return
			}

			rec := &idempotencyResponseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			_ = store.Set(r.Context(), key, rec.buf.Bytes(), rec.status)
		})
	}
}
