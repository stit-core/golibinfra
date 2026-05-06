package httpkit

import "net/http"

type MaxBodyConfig struct {
	MaxBytes int64 `envconfig:"HTTP_MAX_BODY_BYTES" default:"1048576"`
}

func MaxBody(cfg MaxBodyConfig) func(http.Handler) http.Handler {
	if cfg.MaxBytes == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
