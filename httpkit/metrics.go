package httpkit

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// httpInstruments — shared слой для всех middleware'ей. Caller'у не
// нужно прокидывать Meter в каждый middleware: они сами берут глобальный
// otel.Meter (telemetry.Init выставляет правильный provider).
type httpInstruments struct {
	reqTotal           metric.Int64Counter
	reqDuration        metric.Float64Histogram
	panicsTotal        metric.Int64Counter
	maxBodyRejections  metric.Int64Counter
	idempotencyHits    metric.Int64Counter
}

func newHTTPInstruments() *httpInstruments {
	return newHTTPInstrumentsWithMeter(otel.Meter("git.stit.tech/stit-infra/golibinfra/httpkit"))
}

func newHTTPInstrumentsWithMeter(m metric.Meter) *httpInstruments {
	reqTotal, err := m.Int64Counter("http_requests_total",
		metric.WithDescription("Total number of HTTP requests"),
	)
	if err != nil {
		panic(fmt.Sprintf("httpkit: create http_requests_total: %v", err))
	}
	reqDuration, err := m.Float64Histogram("http_request_duration_seconds",
		metric.WithDescription("HTTP request duration in seconds"),
		metric.WithExplicitBucketBoundaries(durationBuckets...),
	)
	if err != nil {
		panic(fmt.Sprintf("httpkit: create http_request_duration_seconds: %v", err))
	}
	panics, err := m.Int64Counter("http_panics_total",
		metric.WithDescription("Panics recovered in HTTP handler chain"),
	)
	if err != nil {
		panic(fmt.Sprintf("httpkit: create http_panics_total: %v", err))
	}
	maxBody, err := m.Int64Counter("http_max_body_rejections_total",
		metric.WithDescription("Requests rejected because body exceeded MaxBytes"),
	)
	if err != nil {
		panic(fmt.Sprintf("httpkit: create http_max_body_rejections_total: %v", err))
	}
	idemp, err := m.Int64Counter("http_idempotency_total",
		metric.WithDescription("Idempotency middleware outcomes (stored|replayed)"),
	)
	if err != nil {
		panic(fmt.Sprintf("httpkit: create http_idempotency_total: %v", err))
	}

	return &httpInstruments{
		reqTotal:          reqTotal,
		reqDuration:       reqDuration,
		panicsTotal:       panics,
		maxBodyRejections: maxBody,
		idempotencyHits:   idemp,
	}
}

// Metrics создаёт middleware, регистрирующий http_requests_total +
// http_request_duration_seconds на переданном Meter. Остальная
// инструментация (Recovery, MaxBody, Idempotency) использует глобальный
// otel.Meter — она не принимает Meter как параметр, чтобы не разрастать
// сигнатуры middleware.
func Metrics(meter metric.Meter) func(http.Handler) http.Handler {
	instr := newHTTPInstrumentsWithMeter(meter)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			path := chi.RouteContext(r.Context()).RoutePattern()
			if path == "" {
				path = r.URL.Path
			}

			attrs := metric.WithAttributes(
				attribute.String("method", r.Method),
				attribute.String("path", path),
				attribute.String("status", strconv.Itoa(rec.status)),
			)
			instr.reqTotal.Add(r.Context(), 1, attrs)
			instr.reqDuration.Record(r.Context(), time.Since(start).Seconds(), attrs)
		})
	}
}
