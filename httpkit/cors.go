package httpkit

import (
	"net/http"
	"strconv"
	"strings"
)

type CORSConfig struct {
	Origins []string `envconfig:"HTTP_CORS_ORIGINS"`
	Methods []string `envconfig:"HTTP_CORS_METHODS" default:"GET,POST,PUT,PATCH,DELETE,OPTIONS"`
	Headers []string `envconfig:"HTTP_CORS_HEADERS" default:"Authorization,Content-Type,X-Request-ID"`
	MaxAge  int      `envconfig:"HTTP_CORS_MAX_AGE" default:"600"`
}

func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	if len(cfg.Origins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	originSet := make(map[string]struct{}, len(cfg.Origins))
	for _, o := range cfg.Origins {
		originSet[strings.TrimSpace(o)] = struct{}{}
	}

	methods := strings.Join(cfg.Methods, ", ")
	headers := strings.Join(cfg.Headers, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			_, allowed := originSet[origin]
			if !allowed {
				for o := range originSet {
					if o == "*" {
						allowed = true
						origin = "*"
						break
					}
				}
			}

			if allowed {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Access-Control-Max-Age", maxAge)

				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
