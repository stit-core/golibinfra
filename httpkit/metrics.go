package httpkit

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

type instruments struct {
	reqTotal          metric.Int64Counter
	reqDuration       metric.Float64Histogram
	panicsTotal       metric.Int64Counter
	maxBodyRejections metric.Int64Counter
	idempotency       metric.Int64Counter
	rateLimited       metric.Int64Counter
}

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
			metric.WithExplicitBucketBoundaries(durationBuckets...))),
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

// Metrics эмитит http_requests_total и http_request_duration_seconds через
// глобальный otel.Meter и даёт server-спану имя «METHOD /route/{param}».
func Metrics() func(http.Handler) http.Handler {
	return metricsWith(newInstruments())
}

func metricsWith(instr *instruments) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := wrap(w)
			next.ServeHTTP(ww, r)

			route := routeLabel(r)
			if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
				span.SetName(r.Method + " " + route)
			}
			attrs := metric.WithAttributes(
				attribute.String("method", r.Method),
				attribute.String("path", route),
				attribute.String("status", strconv.Itoa(ww.status)),
			)
			instr.reqTotal.Add(r.Context(), 1, attrs)
			instr.reqDuration.Record(r.Context(), time.Since(start).Seconds(), attrs)
		})
	}
}
