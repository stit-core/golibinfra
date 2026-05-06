package httpkit

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func BenchmarkSecurityHeaders(b *testing.B) {
	cfg := SecurityHeadersConfig{
		HSTS:               "max-age=31536000; includeSubDomains",
		ContentTypeOptions: "nosniff",
		FrameOptions:       "DENY",
		ReferrerPolicy:     "strict-origin-when-cross-origin",
		CSP:                "default-src 'self'",
	}
	r := chi.NewRouter()
	r.Use(SecurityHeaders(cfg))
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
	}
}
