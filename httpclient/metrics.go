package httpclient

import (
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type instruments struct {
	breakerTransitions metric.Int64Counter
	breakerOpen        metric.Int64UpDownCounter
	retryAttempts      metric.Int64Counter
	requestDuration    metric.Float64Histogram
}

func newInstruments() *instruments {
	m := otelkit.Meter("httpclient")
	return &instruments{
		breakerTransitions: otelkit.Must(m.Int64Counter("http_client_breaker_transitions_total",
			metric.WithDescription("Circuit breaker state transitions per upstream host"))),
		breakerOpen: otelkit.Must(m.Int64UpDownCounter("http_client_breaker_open",
			metric.WithDescription("1 while the host breaker is open"))),
		retryAttempts: otelkit.Must(m.Int64Counter("http_client_retry_attempts_total",
			metric.WithDescription("Retry attempts initiated by httpclient"))),
		requestDuration: otelkit.Must(m.Float64Histogram("http_client_request_duration_seconds",
			metric.WithDescription("Outbound HTTP request duration including retries"),
			metric.WithUnit("s"))),
	}
}
