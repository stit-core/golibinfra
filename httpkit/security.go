package httpkit

import "net/http"

type SecurityHeadersConfig struct {
	HSTS               string `envconfig:"HTTP_HSTS" default:"max-age=31536000; includeSubDomains"`
	ContentTypeOptions string `envconfig:"HTTP_CONTENT_TYPE_OPTIONS" default:"nosniff"`
	FrameOptions       string `envconfig:"HTTP_FRAME_OPTIONS" default:"DENY"`
	ReferrerPolicy     string `envconfig:"HTTP_REFERRER_POLICY" default:"strict-origin-when-cross-origin"`
	CSP                string `envconfig:"HTTP_CSP"`
}

func SecurityHeaders(cfg SecurityHeadersConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			if cfg.HSTS != "" {
				h.Set("Strict-Transport-Security", cfg.HSTS)
			}
			if cfg.ContentTypeOptions != "" {
				h.Set("X-Content-Type-Options", cfg.ContentTypeOptions)
			}
			if cfg.FrameOptions != "" {
				h.Set("X-Frame-Options", cfg.FrameOptions)
			}
			if cfg.ReferrerPolicy != "" {
				h.Set("Referrer-Policy", cfg.ReferrerPolicy)
			}
			if cfg.CSP != "" {
				h.Set("Content-Security-Policy", cfg.CSP)
			}
			next.ServeHTTP(w, r)
		})
	}
}
