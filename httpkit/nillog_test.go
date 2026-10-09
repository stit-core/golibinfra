package httpkit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// Регрессия RAPI-02: nil-логгер — logkit.Nop() у каждой экспортируемой функции кита, как у
// NewServer. Раньше Recovery, AccessLog, ClientIP, HumaAuth и Idempotency паниковали на запросе,
// а RegisterHealth — в горутине пробы: процесс падал на первой упавшей пробе.
func TestNilLoggerIsNop(t *testing.T) {
	serve := func(h http.Handler, method, target string, headers ...string) int {
		t.Helper()
		req := httptest.NewRequest(method, target, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	t.Run("Recovery", func(t *testing.T) {
		h := httpkit.Recovery(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
		if code := serve(h, http.MethodGet, "/"); code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 from the recovered panic", code)
		}
	})
	t.Run("AccessLog", func(t *testing.T) {
		if code := serve(httpkit.AccessLog(nil)(ok), http.MethodGet, "/"); code != http.StatusNoContent {
			t.Fatalf("status = %d, want the handler's 204", code)
		}
	})
	t.Run("ClientIP", func(t *testing.T) {
		if code := serve(httpkit.ClientIP(0, nil)(ok), http.MethodGet, "/", "X-Forwarded-For", "203.0.113.7"); code != http.StatusNoContent {
			t.Fatalf("status = %d, want the handler's 204", code)
		}
	})
	t.Run("Idempotency", func(t *testing.T) {
		cfg := httpkit.IdempotencyConfig{Header: "Idempotency-Key", TTL: time.Minute, ReserveTTL: time.Minute, MaxBodyBytes: 64, StoreTimeout: time.Second}
		h := httpkit.Idempotency(cfg, downStore{}, nil)(ok)
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Idempotency-Key", "k")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(httpkit.ContextWithSubject(req.Context(), "alice")))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want the handler's 204 after the store failure (fail-open)", rec.Code)
		}
	})
	t.Run("HumaAuth", func(t *testing.T) {
		_, api := humatest.New(t, huma.DefaultConfig("t", "0"))
		reject := func(*http.Request) (context.Context, error) { return nil, errors.New("bad token") }
		api.UseMiddleware(httpkit.HumaAuth([]httpkit.SecuritySchemeEntry{{Name: "Bearer", Scheme: bearerScheme, Extract: reject}}, nil))
		huma.Register(api, huma.Operation{OperationID: "me", Method: http.MethodGet, Path: "/me", Security: bearerOnly}, noBody)
		huma.Register(api, huma.Operation{OperationID: "key", Method: http.MethodGet, Path: "/key", Security: []map[string][]string{{"Key": {}}}}, noBody)
		if code := api.Get("/me").Code; code != http.StatusUnauthorized {
			t.Fatalf("rejected credentials: %d, want 401", code)
		}
		if code := api.Get("/key").Code; code != http.StatusInternalServerError {
			t.Fatalf("scheme without an extractor: %d, want 500", code)
		}
	})
	t.Run("RegisterHealth", func(t *testing.T) {
		_, api := humatest.New(t, huma.DefaultConfig("t", "0"))
		httpkit.RegisterHealth(api, time.Second, nil,
			healthkit.Probe{Name: "down", Check: func(context.Context) error { return errors.New("connection refused") }},
			healthkit.Probe{Name: "panics", Check: func(context.Context) error { panic("boom") }})
		if code := api.Get("/ready").Code; code != http.StatusServiceUnavailable {
			t.Fatalf("/ready with failing probes: %d, want 503", code)
		}
	})
}
