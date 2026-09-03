package httpkit_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func TestCORS_Preflight(t *testing.T) {
	cfg := httpkit.CORSConfig{
		Origins: []string{"https://example.com"},
		Methods: []string{"GET", "POST"},
		Headers: []string{"Authorization", "Content-Type"},
		MaxAge:  600,
	}
	h := httpkit.CORS(cfg)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight: want 204, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Errorf("ACAO header not set: %s", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_RegularRequest(t *testing.T) {
	cfg := httpkit.CORSConfig{
		Origins: []string{"https://example.com"},
		Methods: []string{"GET"},
		Headers: []string{"Content-Type"},
		MaxAge:  600,
	}
	h := httpkit.CORS(cfg)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Errorf("ACAO header not set")
	}
}

func TestCORS_Disabled(t *testing.T) {
	cfg := httpkit.CORSConfig{}
	h := httpkit.CORS(cfg)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS should be disabled when origins empty")
	}
}

func TestRateLimit_AllowAndReject(t *testing.T) {
	// RPS=1: за время 101 запроса ведро не успевает пополниться, 101-й всегда 429.
	cfg := httpkit.RateLimitConfig{RPS: 1, Burst: 100}
	h := httpkit.RateLimit(cfg)(http.HandlerFunc(okHandler))

	srv := httptest.NewServer(h)
	defer srv.Close()

	var last int
	for i := 0; i < 101; i++ {
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		last = resp.StatusCode
	}

	if last != http.StatusTooManyRequests {
		t.Errorf("101st request: want 429, got %d", last)
	}
}

func TestRateLimit_Disabled(t *testing.T) {
	cfg := httpkit.RateLimitConfig{RPS: 0}
	h := httpkit.RateLimit(cfg)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200 when disabled, got %d", rec.Code)
	}
}

func TestMaxBody_OK(t *testing.T) {
	cfg := httpkit.MaxBodyConfig{MaxBytes: 1 << 20}

	var received []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		received = b
		w.WriteHeader(http.StatusOK)
	})
	h := httpkit.MaxBody(cfg)(handler)

	body := bytes.Repeat([]byte("a"), 1<<20)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
	if len(received) != 1<<20 {
		t.Errorf("want %d bytes, got %d", 1<<20, len(received))
	}
}

func TestMaxBody_TooLarge(t *testing.T) {
	cfg := httpkit.MaxBodyConfig{MaxBytes: 1 << 20}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			if strings.Contains(err.Error(), "too large") || strings.Contains(err.Error(), "request body too large") {
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := httpkit.MaxBody(cfg)(handler)

	body := bytes.Repeat([]byte("a"), 2<<20)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("want 413, got %d", rec.Code)
	}
}
