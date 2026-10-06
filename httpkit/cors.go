package httpkit

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig — политика CORS; пустой Origins выключает middleware.
type CORSConfig struct {
	// Origins пуст — CORS выключен. «*» разрешает любой origin (без credentials).
	Origins []string `envconfig:"HTTP_CORS_ORIGINS"`
	Methods []string `envconfig:"HTTP_CORS_METHODS" default:"GET,POST,PUT,PATCH,DELETE,OPTIONS"`
	Headers []string `envconfig:"HTTP_CORS_HEADERS" default:"Authorization,Content-Type,X-Request-ID,Idempotency-Key"`
	// ExposeHeaders — заголовки ответа, видимые cross-origin JS.
	ExposeHeaders    []string `envconfig:"HTTP_CORS_EXPOSE_HEADERS" default:"X-Request-ID,Location,Retry-After,Idempotent-Replayed"`
	MaxAge           int      `envconfig:"HTTP_CORS_MAX_AGE" default:"600"`
	AllowCredentials bool     `envconfig:"HTTP_CORS_ALLOW_CREDENTIALS" default:"false"`
}

// CORS отвечает на preflight и проставляет CORS-заголовки разрешённым origin.
// Origin, разрешённый только «*», всегда получает `*` без credentials: отражать
// произвольный origin вместе с credentials небезопасно.
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	if len(cfg.Origins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	any := false
	originSet := make(map[string]struct{}, len(cfg.Origins))
	for _, o := range cfg.Origins {
		o = strings.TrimSpace(o)
		if o == "*" {
			any = true
			continue
		}
		originSet[o] = struct{}{}
	}

	methods := strings.Join(cfg.Methods, ", ")
	headers := strings.Join(cfg.Headers, ", ")
	expose := strings.Join(cfg.ExposeHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			h := w.Header()
			// Ответ зависит от Origin: кэш не должен отдать чужой ACAO.
			h.Add("Vary", "Origin")

			_, listed := originSet[origin]
			allowed := origin != "" && (listed || any)
			if allowed {
				echo := origin
				if any && !listed {
					echo = "*"
				}
				h.Set("Access-Control-Allow-Origin", echo)
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Access-Control-Max-Age", maxAge)
				if expose != "" {
					h.Set("Access-Control-Expose-Headers", expose)
				}
				if cfg.AllowCredentials && echo != "*" {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
