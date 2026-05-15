package httpclient

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

type httpClientInstruments struct {
	breakerTransitions metric.Int64Counter
	breakerState       metric.Int64UpDownCounter
	retryAttempts      metric.Int64Counter
	requestDuration    metric.Float64Histogram
}

func newInstruments() *httpClientInstruments {
	m := otel.Meter("git.stit.tech/stit-infra/golibinfra/httpclient")

	transitions, err := m.Int64Counter("http_client_breaker_transitions_total",
		metric.WithDescription("Circuit breaker state transitions"),
	)
	if err != nil {
		panic(fmt.Sprintf("httpclient: counter transitions: %v", err))
	}
	state, err := m.Int64UpDownCounter("http_client_breaker_open",
		metric.WithDescription("1 if breaker is open, 0 if closed/half-open"),
	)
	if err != nil {
		panic(fmt.Sprintf("httpclient: gauge breaker_open: %v", err))
	}
	retries, err := m.Int64Counter("http_client_retry_attempts_total",
		metric.WithDescription("Retry attempts initiated by httpclient"),
	)
	if err != nil {
		panic(fmt.Sprintf("httpclient: counter retries: %v", err))
	}
	dur, err := m.Float64Histogram("http_client_request_duration_seconds",
		metric.WithDescription("Outbound HTTP request duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(fmt.Sprintf("httpclient: histogram duration: %v", err))
	}

	return &httpClientInstruments{
		breakerTransitions: transitions,
		breakerState:       state,
		retryAttempts:      retries,
		requestDuration:    dur,
	}
}
