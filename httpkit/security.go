package httpkit

import "net/http"

type SecurityHeadersConfig struct {
	HSTS               string `envconfig:"HTTP_HSTS" default:"max-age=31536000; includeSubDomains"`
	ContentTypeOptions string `default:"nosniff"`
	FrameOptions       string `default:"DENY"`
	ReferrerPolicy     string `default:"strict-origin-when-cross-origin"`
	CSP                string `envconfig:"HTTP_CSP"`
}

func SecurityHeaders(cfg SecurityHeadersConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.HSTS != "" {
				w.Header().Set("Strict-Transport-Security", cfg.HSTS)
			}
			if cfg.ContentTypeOptions != "" {
				w.Header().Set("X-Content-Type-Options", cfg.ContentTypeOptions)
			}
			if cfg.FrameOptions != "" {
				w.Header().Set("X-Frame-Options", cfg.FrameOptions)
			}
			if cfg.ReferrerPolicy != "" {
				w.Header().Set("Referrer-Policy", cfg.ReferrerPolicy)
			}
			if cfg.CSP != "" {
				w.Header().Set("Content-Security-Policy", cfg.CSP)
			}
			next.ServeHTTP(w, r)
		})
	}
}
