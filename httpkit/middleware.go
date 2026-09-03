package httpkit

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"git.stit.tech/stit-core/golibinfra/idkit"
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func init() {
	logkit.RegisterAttr(func(ctx context.Context) (string, any, bool) {
		if id := RequestIDFrom(ctx); id != "" {
			return "request_id", id, true
		}
		return "", nil, false
	})
}

const HeaderRequestID = requestid.Header

func RequestIDFrom(ctx context.Context) string { return requestid.From(ctx) }

// ContextWithRequestID кладёт request-id под общий ключ: HTTP-middleware и
// gRPC-интерсептор (grpckit) пишут одно и то же место.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return requestid.With(ctx, id)
}

// RequestID принимает X-Request-ID клиента только печатным ASCII до 128 байт,
// иначе выдаёт свой UUIDv7: id попадает в каждую строку лога.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if !requestid.Valid(id) {
				id = idkit.NewV7()
			}
			w.Header().Set(HeaderRequestID, id)
			next.ServeHTTP(w, r.WithContext(ContextWithRequestID(r.Context(), id)))
		})
	}
}

// Recovery переводит panic в 500; http.ErrAbortHandler пробрасывает дальше —
// это штатный способ оборвать соединение.
func Recovery(log logkit.Logger) func(http.Handler) http.Handler {
	instr := newInstruments()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := wrap(w)
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				log.Error(r.Context(), "panic recovered",
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				instr.panicsTotal.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("path", routeLabel(r)),
				))
				if !ww.wroteHeader {
					writeAPIError(ww, Internal("INTERNAL", "internal server error"))
				}
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

// AccessLog пишет одну строку на запрос; request_id и trace_id добавляет logkit
// из ctx, поэтому middleware стоит после RequestID и OTel.
func AccessLog(log logkit.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := wrap(w)
			next.ServeHTTP(ww, r)

			kv := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"route", routeLabel(r),
				"status", ww.status,
				"bytes", ww.bytes,
				"dur", time.Since(start),
			}
			switch {
			case ww.status >= 500:
				log.Error(r.Context(), "http", kv...)
			case ww.status >= 400:
				log.Warn(r.Context(), "http", kv...)
			default:
				log.Info(r.Context(), "http", kv...)
			}
		})
	}
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

// OTel открывает server-спан и извлекает входящий traceparent. RED-метрики
// otelhttp выключены: их эмитит Metrics в одном словаре с остальными китами.
// Имя спана уточняет Metrics после роутинга.
func OTel() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "http",
			otelhttp.WithMeterProvider(metricnoop.NewMeterProvider()),
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }),
		)
	}
}
