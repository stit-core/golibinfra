package httpkit

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const HeaderRequestID = "X-Request-ID"

type requestIDKey struct{}

func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

// RequestID — берёт из заголовка либо генерит UUID; кладёт в ctx и в response.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if id == "" {
				id = uuid.NewString()
			}
			w.Header().Set(HeaderRequestID, id)
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Recovery — ловит панику, пишет 500, не валит процесс.
func Recovery(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.ErrorContext(r.Context(), "panic recovered",
						slog.Any("panic", rec),
						slog.String("stack", string(debug.Stack())),
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog — структурный лог запроса; уровень зависит от статуса.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			lvl := slog.LevelInfo
			if rec.status >= 500 {
				lvl = slog.LevelError
			} else if rec.status >= 400 {
				lvl = slog.LevelWarn
			}
			log.LogAttrs(r.Context(), lvl, "http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Duration("dur", time.Since(start)),
				slog.String("request_id", RequestIDFrom(r.Context())),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}

// AuthExtractor — продукт-специфичная функция: берёт креды из запроса и обогащает ctx (например, кладёт identity).
type AuthExtractor func(r *http.Request) (context.Context, error)

// Auth — обёртка над AuthExtractor; при ошибке отвечает 401, иначе пробрасывает обогащённый ctx дальше.
// Если extract == nil — middleware no-op (полезно когда auth применяется не глобально, а на конкретные роуты).
func Auth(extract AuthExtractor) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if extract == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, err := extract(r)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// OTel — инструментация HTTP-обработчиков спанами и метриками через otelhttp.
func OTel() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "http")
	}
}
