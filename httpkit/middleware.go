package httpkit

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"

	"git.stit.tech/stit-core/golibinfra/idkit"
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func init() {
	logkit.RegisterAttr("request_id", func(ctx context.Context) (any, bool) {
		id := RequestIDFrom(ctx)
		return id, id != ""
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

// Recovery переводит panic в 500; после записанного заголовка — отправляет начатое и паникует
// http.ErrAbortHandler: net/http обрывает соединение, и усечённый ответ не выглядит полным. Свой
// http.ErrAbortHandler хендлера пробрасывает дальше.
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
				if ww.wroteHeader {
					// Начатый ответ уходит клиенту и обрывается: без Flush net/http закрыл бы
					// соединение до заголовков, и сбой выглядел бы сетевой ошибкой до ответа.
					ww.Flush()
					panic(http.ErrAbortHandler)
				}
				writeAPIError(r.Context(), ww, neutral(http.StatusInternalServerError, nil))
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

// AuthExtractor проверяет credentials своей схемы безопасности (SecuritySchemeEntry.Extract) и
// возвращает ctx с личностью; ошибка → 401, обёрнутый ErrAuthUnavailable → 503.
type AuthExtractor func(r *http.Request) (context.Context, error)

// ErrAuthUnavailable — зависимость аутентификации недоступна (JWKS, хаб,
// session store). Extractor оборачивает её, чтобы клиент получил 503 и не
// разлогинивал пользователя по 401.
var ErrAuthUnavailable = errors.New("httpkit: auth backend unavailable")

// codeUnauthorized — code 401 HumaAuth: его же операция с Security объявляет в OpenAPI.
const codeUnauthorized = "UNAUTHORIZED"

// ErrAuthNotConfigured — у схемы из Security операции нет extractor'а: Module не стартует,
// HumaAuth отвечает на такую операцию 500 AUTH_NOT_CONFIGURED.
var ErrAuthNotConfigured = errors.New("httpkit: auth not configured")

// HumaAuth проверяет операции с непустым Operation().Security extractor'ами их схем (schemes):
// операция открыта, если прошла одна из её альтернатив целиком — каждая схема альтернативы, в
// порядке имён, и ctx с личностью — от неё. Пустая альтернатива `{}` (необязательная
// аутентификация) идёт последней, в каком бы месте ни стояла: запрос, не прошедший схемы,
// открыт без личности. Схема без extractor'а альтернативу не открывает: если открыть нечем, —
// 500 AUTH_NOT_CONFIGURED. 401 несёт WWW-Authenticate по схемам операции; отказ бэкенда любой
// схемы — 503, и при `{}`: предъявленные credentials не превращаются в анонима. Работает на
// любом huma-адаптере (chi в проде, humatest в тестах потребителя).
func HumaAuth(schemes []SecuritySchemeEntry, log logkit.Logger) func(huma.Context, func(huma.Context)) {
	extractors := make(map[string]AuthExtractor, len(schemes))
	for _, e := range schemes {
		if e.Extract != nil {
			extractors[e.Name] = e.Extract
		}
	}
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		if len(op.Security) == 0 {
			next(ctx)
			return
		}
		var failures []error
		optional := false
		for _, alt := range op.Security {
			if len(alt) == 0 {
				optional = true
				continue
			}
			authCtx, configured, err := authenticate(requestFrom(ctx), extractors, alt)
			if !configured {
				continue
			}
			if err == nil {
				next(huma.WithContext(ctx, authCtx))
				return
			}
			failures = append(failures, err)
		}
		if optional && !slices.ContainsFunc(failures, func(err error) bool { return errors.Is(err, ErrAuthUnavailable) }) {
			next(ctx)
			return
		}
		if len(failures) == 0 {
			log.Error(ctx.Context(), "auth not configured", "operation", op.OperationID)
			writeHumaAPIError(ctx, Error("AUTH_NOT_CONFIGURED", http.StatusInternalServerError, ""))
			return
		}
		e := authError(ctx.Context(), log, failures)
		if c := operationChallenge(op); e.Status == http.StatusUnauthorized && c != "" {
			ctx.SetHeader("WWW-Authenticate", c)
		}
		writeHumaAPIError(ctx, e)
	}
}

// authenticate проходит альтернативу Security: extractor каждой её схемы в порядке имён, ctx — от
// предыдущей. configured=false — у схемы альтернативы нет extractor'а.
func authenticate(r *http.Request, extractors map[string]AuthExtractor, alt map[string][]string) (ctx context.Context, configured bool, err error) {
	ctx = r.Context()
	for _, name := range slices.Sorted(maps.Keys(alt)) {
		extract, ok := extractors[name]
		if !ok {
			return nil, false, nil
		}
		if ctx, err = extract(r.WithContext(ctx)); err != nil {
			return nil, true, err
		}
	}
	return ctx, true, nil
}

// challengeKey — ключ Operation.Metadata с вызовом WWW-Authenticate операции (authChallenge).
const challengeKey = "httpkit.challenge"

// authChallenge — хук OnAddOperation кита: вызов WWW-Authenticate для 401 операции по её схемам в
// components (RFC 9110 §11.6.1).
func authChallenge(oapi *huma.OpenAPI, op *huma.Operation) {
	if len(op.Security) == 0 {
		return
	}
	op.Metadata = maps.Clone(op.Metadata)
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[challengeKey] = challenge(oapi.Components.SecuritySchemes, op.Security)
}

// operationChallenge — вызов 401 операции: из хука кита, а у операции чужого API — по именам схем.
func operationChallenge(op *huma.Operation) string {
	if c, ok := op.Metadata[challengeKey].(string); ok {
		return c
	}
	return challenge(nil, op.Security)
}

// challenge — схемы аутентификации требований операции без повторов: http — своя (`Bearer`,
// `Basic`), oauth2 и openIdConnect — `Bearer`, прочие (apiKey) и схема вне components — её имя.
func challenge(schemes map[string]*huma.SecurityScheme, security []map[string][]string) string {
	var out []string
	for _, alt := range security {
		for _, name := range slices.Sorted(maps.Keys(alt)) {
			c := name
			if s := schemes[name]; s != nil {
				switch s.Type {
				case "http":
					if s.Scheme != "" {
						c = strings.ToUpper(s.Scheme[:1]) + s.Scheme[1:]
					}
				case "oauth2", "openIdConnect":
					c = "Bearer"
				}
			}
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return strings.Join(out, ", ")
}

// authError переводит отказы альтернатив в один APIError и логирует причины: отказ бэкенда — 503,
// иначе APIError extractor'а (свой статус и код), иначе 401; наружу уходит только код.
func authError(ctx context.Context, log logkit.Logger, failures []error) *APIError {
	var apiErr *APIError
	for _, err := range failures {
		if errors.Is(err, ErrAuthUnavailable) {
			log.Warn(ctx, "auth backend unavailable", "error", err)
			return Unavailable("AUTH_UNAVAILABLE", "")
		}
	}
	for _, err := range failures {
		if errors.As(err, &apiErr) {
			if apiErr.Status >= http.StatusInternalServerError {
				log.Error(ctx, "auth failed", "error", err, "code", apiErr.Code)
			} else {
				log.Debug(ctx, "auth rejected", "error", err, "code", apiErr.Code)
			}
			return apiErr
		}
	}
	log.Debug(ctx, "auth rejected", "error", errors.Join(failures...))
	return Unauthorized(codeUnauthorized, "unauthorized")
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

// OTel открывает server-спан. Входящий trace-context клиента без доверия (trustTraceContext,
// HTTP_TRUST_TRACE_CONTEXT) — ссылка на спане нового корня, а baggage не читается: клиент не
// выбирает trace_id, не включает сэмплирование и не кладёт значения в ctx. С доверием спан —
// ребёнок входящего контекста, baggage — по глобальному propagator'у. RED-метрики otelhttp
// выключены: их эмитит Metrics в одном словаре с остальными китами. Запросы на quietPaths (пробы
// оркестратора) спан не открывают.
func OTel(trustTraceContext bool, quietPaths ...string) func(http.Handler) http.Handler {
	quiet := pathSet(quietPaths)
	opts := []otelhttp.Option{
		otelhttp.WithMeterProvider(metricnoop.NewMeterProvider()),
		otelhttp.WithSpanNameFormatter(spanName),
		otelhttp.WithFilter(func(r *http.Request) bool {
			_, isQuiet := quiet[r.URL.Path]
			return !isQuiet
		}),
	}
	if !trustTraceContext {
		opts = append(opts,
			otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true }),
			otelhttp.WithPropagators(propagation.TraceContext{}))
	}
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "http", opts...)
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
