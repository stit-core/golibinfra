package httpkit

import (
	"context"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"git.stit.tech/stit/golibinfra/logkit"
)

func init() {
	logkit.RegisterAttr(func(ctx context.Context) (string, any, bool) {
		if id := RequestIDFrom(ctx); id != "" {
			return "request_id", id, true
		}
		return "", nil, false
	})
}

const HeaderRequestID = "X-Request-ID"

type requestIDKey struct{}

func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

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

func Recovery(log logkit.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error(
						r.Context(), "panic recovered",
						"panic", rec,
						"stack", string(debug.Stack()),
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func AccessLog(log logkit.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			// request_id не дублируем — Attr-registrar в init() добавит автоматом
			// в любом ctx-scoped лог-вызове.
			kv := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"dur", time.Since(start),
			}
			switch {
			case rec.status >= 500:
				log.Error(r.Context(), "http", kv...)
			case rec.status >= 400:
				log.Warn(r.Context(), "http", kv...)
			default:
				log.Info(r.Context(), "http", kv...)
			}
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

type AuthExtractor func(r *http.Request) (context.Context, error)

func HumaAuth(api huma.API, extract AuthExtractor) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if extract == nil || len(ctx.Operation().Security) == 0 {
			next(ctx)
			return
		}
		r, w := humachi.Unwrap(ctx)
		newCtx, err := extract(r)
		if err != nil {
			_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(humachi.NewContext(ctx.Operation(), r.WithContext(newCtx), w))
	}
}

func OTel() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "http")
	}
}
