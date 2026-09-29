package httpkit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"git.stit.tech/stit-core/golibinfra/idkit"
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func init() {
	logkit.RegisterAttr("request_id", func(ctx context.Context) (slog.Value, bool) {
		if id := RequestIDFrom(ctx); id != "" {
			return slog.StringValue(id), true
		}
		return slog.Value{}, false
	})
}

// HeaderRequestID — заголовок корреляции запроса, общий с grpckit и httpclient.
const HeaderRequestID = requestid.Header

// RequestIDFrom возвращает request-id из ctx; пустая строка, если его нет.
func RequestIDFrom(ctx context.Context) string { return requestid.From(ctx) }

// ContextWithRequestID кладёт request-id под общий ключ: HTTP-middleware и
// gRPC-интерсептор (grpckit) пишут одно и то же место.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return requestid.With(ctx, id)
}

type subjectCtxKey struct{}

// ContextWithSubject кладёт идентификатор аутентифицированного субъекта в ctx:
// по нему scope'ится идемпотентность вместо сырых credentials.
func ContextWithSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, subjectCtxKey{}, subject)
}

// SubjectFrom возвращает идентификатор субъекта, положенный ContextWithSubject.
func SubjectFrom(ctx context.Context) string {
	s, _ := ctx.Value(subjectCtxKey{}).(string)
	return s
}

type authDoneCtxKey struct{}

// withAuthDone помечает ctx как уже аутентифицированный: Idempotency вызывает
// extractor до replay, и HumaAuth не должен верифицировать те же credentials второй раз.
func withAuthDone(ctx context.Context) context.Context {
	return context.WithValue(ctx, authDoneCtxKey{}, true)
}

func authDone(ctx context.Context) bool {
	done, _ := ctx.Value(authDoneCtxKey{}).(bool)
	return done
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
					writeAPIError(r.Context(), ww, neutral(http.StatusInternalServerError, nil))
				}
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

func pathSet(paths []string) map[string]struct{} {
	if len(paths) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if p != "" {
			set[p] = struct{}{}
		}
	}
	return set
}

// AccessLog пишет одну строку на запрос; request_id и trace_id добавляет logkit
// из ctx, поэтому middleware стоит после RequestID и OTel. Успешные ответы на
// quietPaths (пробы оркестратора) логируются на Debug.
func AccessLog(log logkit.Logger, quietPaths ...string) func(http.Handler) http.Handler {
	quiet := pathSet(quietPaths)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := wrap(w)
			// defer: panic ниже по цепочке тоже должна дать строку запроса.
			defer func() {
				kv := []any{
					"method", r.Method,
					"path", r.URL.Path,
					"route", routeLabel(r),
					"status", ww.status,
					"bytes", ww.bytes,
					"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
				}
				_, isQuiet := quiet[r.URL.Path]
				switch {
				case ww.status >= 500:
					log.Error(r.Context(), "http", kv...)
				case ww.status >= 400:
					log.Warn(r.Context(), "http", kv...)
				case isQuiet:
					log.Debug(r.Context(), "http", kv...)
				default:
					log.Info(r.Context(), "http", kv...)
				}
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

// AuthExtractor проверяет credentials запроса и возвращает ctx с личностью;
// ошибка → 401, обёрнутый ErrAuthUnavailable → 503.
type AuthExtractor func(r *http.Request) (context.Context, error)

// ErrAuthUnavailable — зависимость аутентификации недоступна (JWKS, хаб,
// session store). Extractor оборачивает её, чтобы клиент получил 503 и не
// разлогинивал пользователя по 401.
var ErrAuthUnavailable = errors.New("httpkit: auth backend unavailable")

// codeUnauthorized — code 401 HumaAuth: его же операция с Security объявляет в OpenAPI.
const codeUnauthorized = "UNAUTHORIZED"

// ErrAuthNotConfigured — у операции с Security нет AuthExtractor: Module не
// стартует, HumaAuth(log, nil) отвечает на такую операцию 500 AUTH_NOT_CONFIGURED.
var ErrAuthNotConfigured = errors.New("httpkit: auth not configured")

// HumaAuth проверяет операции с непустым Operation().Security: extractor кладёт
// личность в ctx операции; без extractor'а такая операция закрыта (500). Работает
// на любом huma-адаптере (chi в проде, humatest в тестах потребителя).
func HumaAuth(log logkit.Logger, extract AuthExtractor) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if len(ctx.Operation().Security) == 0 || authDone(ctx.Context()) {
			next(ctx)
			return
		}
		if extract == nil {
			log.Error(ctx.Context(), "auth not configured", "operation", ctx.Operation().OperationID)
			writeHumaAPIError(ctx, Error("AUTH_NOT_CONFIGURED", http.StatusInternalServerError, ""))
			return
		}
		newCtx, err := extract(requestFrom(ctx))
		if err != nil {
			writeHumaAPIError(ctx, authError(ctx.Context(), log, err))
			return
		}
		next(huma.WithContext(ctx, withAuthDone(newCtx)))
	}
}

// authError переводит ошибку extractor'а в APIError и логирует причину: наружу
// уходит только код, текст ошибки остаётся у оператора.
func authError(ctx context.Context, log logkit.Logger, err error) *APIError {
	var apiErr *APIError
	switch {
	case errors.Is(err, ErrAuthUnavailable):
		log.Warn(ctx, "auth backend unavailable", "err", err)
		return Unavailable("AUTH_UNAVAILABLE", "")
	case errors.As(err, &apiErr):
		log.Debug(ctx, "auth rejected", "err", err, "code", apiErr.Code)
		return apiErr
	default:
		log.Debug(ctx, "auth rejected", "err", err)
		return Unauthorized(codeUnauthorized, "unauthorized")
	}
}

func writeHumaAPIError(ctx huma.Context, e *APIError) {
	body := e.forRequest(ctx.Context())
	if ra := body.retryAfter(); ra != "" {
		ctx.SetHeader("Retry-After", ra)
	}
	ctx.SetHeader("Content-Type", ProblemContentType)
	ctx.SetStatus(body.Status)
	_ = json.NewEncoder(ctx.BodyWriter()).Encode(body)
}

// unwrapHTTP достаёт запрос и writer адаптера под huma.Context (humachi в
// проде, humaflow в humatest); у адаптера без Unwrap — nil.
func unwrapHTTP(hc huma.Context) (*http.Request, http.ResponseWriter) {
	c := hc
	for {
		u, ok := c.(interface{ Unwrap() huma.Context })
		if !ok {
			break
		}
		c = u.Unwrap()
	}
	if u, ok := c.(interface {
		Unwrap() (*http.Request, http.ResponseWriter)
	}); ok {
		return u.Unwrap()
	}
	return nil, nil
}

// requestFrom достаёт *http.Request адаптера; адаптеру без Unwrap запрос
// собирается из полей huma.Context.
func requestFrom(hc huma.Context) *http.Request {
	if r, _ := unwrapHTTP(hc); r != nil {
		return r.WithContext(hc.Context())
	}

	u := hc.URL()
	r := (&http.Request{
		Method:     hc.Method(),
		URL:        &u,
		Header:     http.Header{},
		Host:       hc.Host(),
		RemoteAddr: hc.RemoteAddr(),
		TLS:        hc.TLS(),
		Body:       http.NoBody,
	}).WithContext(hc.Context())
	hc.EachHeader(func(name, value string) { r.Header.Add(name, value) })
	return r
}

// OTel открывает server-спан и извлекает входящий traceparent. RED-метрики
// otelhttp выключены: их эмитит Metrics в одном словаре с остальными китами.
// Запросы на quietPaths (пробы оркестратора) спан не открывают.
func OTel(quietPaths ...string) func(http.Handler) http.Handler {
	quiet := pathSet(quietPaths)
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "http",
			otelhttp.WithMeterProvider(metricnoop.NewMeterProvider()),
			otelhttp.WithSpanNameFormatter(spanName),
			otelhttp.WithFilter(func(r *http.Request) bool {
				_, isQuiet := quiet[r.URL.Path]
				return !isQuiet
			}),
		)
	}
}

// spanName вызывается otelhttp дважды: до роутинга (паттерн неизвестен) и
// после, когда chi выставил r.Pattern — тогда имя становится «METHOD /route».
func spanName(_ string, r *http.Request) string {
	method := methodLabel(r.Method)
	if route := routeLabel(r); route != "unmatched" {
		return method + " " + route
	}
	return method
}
