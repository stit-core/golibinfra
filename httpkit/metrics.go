package httpkit

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

func Metrics(meter metric.Meter) func(http.Handler) http.Handler {
	reqTotal, err := meter.Int64Counter("http_requests_total",
		metric.WithDescription("Total number of HTTP requests"),
	)
	if err != nil {
		panic(fmt.Sprintf("httpkit: create http_requests_total counter: %v", err))
	}

	reqDuration, err := meter.Float64Histogram("http_request_duration_seconds",
		metric.WithDescription("HTTP request duration in seconds"),
		metric.WithExplicitBucketBoundaries(durationBuckets...),
	)
	if err != nil {
		panic(fmt.Sprintf("httpkit: create http_request_duration_seconds histogram: %v", err))
	}

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

			reqTotal.Add(r.Context(), 1, attrs)
			reqDuration.Record(r.Context(), time.Since(start).Seconds(), attrs)
		})
	}
}
