package httpkit

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type instruments struct {
	reqTotal          metric.Int64Counter
	reqDuration       metric.Float64Histogram
	panicsTotal       metric.Int64Counter
	maxBodyRejections metric.Int64Counter
	idempotency       metric.Int64Counter
	rateLimited       metric.Int64Counter
}

// newInstruments зовётся при сборке middleware: инструмент берёт текущий
// глобальный провайдер, а не первый провайдер процесса.
func newInstruments() *instruments {
	return newInstrumentsWithMeter(otelkit.Meter("httpkit"))
}

func newInstrumentsWithMeter(m metric.Meter) *instruments {
	return &instruments{
		reqTotal: otelkit.Must(m.Int64Counter("http_requests_total",
			metric.WithDescription("HTTP requests by method, route pattern and status"))),
		reqDuration: otelkit.Must(m.Float64Histogram("http_request_duration_seconds",
			metric.WithDescription("HTTP request duration"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets())),
		panicsTotal: otelkit.Must(m.Int64Counter("http_panics_total",
			metric.WithDescription("Panics recovered in the HTTP handler chain"))),
		maxBodyRejections: otelkit.Must(m.Int64Counter("http_max_body_rejections_total",
			metric.WithDescription("Requests rejected because the body exceeded MaxBytes"))),
		idempotency: otelkit.Must(m.Int64Counter("http_idempotency_total",
			metric.WithDescription("Idempotency middleware outcomes"))),
		rateLimited: otelkit.Must(m.Int64Counter("http_rate_limited_total",
			metric.WithDescription("Requests rejected by the per-client rate limiter"))),
	}
}

// routeLabel — chi-паттерн маршрута после роутинга; для неизвестного пути
// «unmatched», чтобы сканы 404 не раздували кардинальность.
func routeLabel(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}

var knownMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace,
}

// methodLabel — метод из конечного множества: клиент отправляет произвольный
// токен, а label метрики и имя спана должны оставаться перечислимыми.
func methodLabel(m string) string {
	if slices.Contains(knownMethods, m) {
		return m
	}
	return "other"
}

// Metrics эмитит http_requests_total и http_request_duration_seconds через
// глобальный otel.Meter и дополняет server-спан: имя «METHOD /route», http.route,
// request_id и client.address из ClientIP.
func Metrics() func(http.Handler) http.Handler {
	return metricsWith(newInstruments())
}

func metricsWith(instr *instruments) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := wrap(w)
			// defer: panic ниже по цепочке тоже должна дать сэмпл.
			defer func() {
				route := routeLabel(r)
				if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
					// Имя — здесь, а не форматтером otelhttp: тот видит маршрут, только если до
					// роутера дошёл его объект запроса, а middleware с r.WithContext его заменяет.
					if route != "unmatched" {
						span.SetName(methodLabel(r.Method) + " " + route)
					}
					attrs := []attribute.KeyValue{semconv.HTTPRoute(route)}
					if id := RequestIDFrom(r.Context()); id != "" {
						attrs = append(attrs, attribute.String("request_id", id))
					}
					// otelhttp берёт client.address из X-Forwarded-For клиента.
					if ip := ClientIPFrom(r.Context()); ip != "" {
						attrs = append(attrs, semconv.ClientAddress(ip))
					}
					span.SetAttributes(attrs...)
				}
				attrs := metric.WithAttributes(
					attribute.String("method", methodLabel(r.Method)),
					attribute.String("path", route),
					attribute.String("status", strconv.Itoa(ww.status)),
				)
				instr.reqTotal.Add(r.Context(), 1, attrs)
				instr.reqDuration.Record(r.Context(), time.Since(start).Seconds(), attrs)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}
