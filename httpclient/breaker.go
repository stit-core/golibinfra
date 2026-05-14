package httpclient

import (
	"context"
	"net/http"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type BreakerConfig struct {
	FailureThreshold int           `envconfig:"HTTPCLIENT_BREAKER_FAILURE_THRESHOLD" default:"5"`
	RecoveryTimeout  time.Duration `envconfig:"HTTPCLIENT_BREAKER_RECOVERY_TIMEOUT" default:"30s"`
	SuccessThreshold int           `envconfig:"HTTPCLIENT_BREAKER_SUCCESS_THRESHOLD" default:"3"`
}

func newBreaker(cfg BreakerConfig, instr *httpClientInstruments) *gobreaker.CircuitBreaker[*http.Response] {
	st := gobreaker.Settings{
		Name:    "httpclient",
		Timeout: cfg.RecoveryTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return int(counts.ConsecutiveFailures) >= cfg.FailureThreshold
		},
		IsSuccessful: func(err error) bool {
			return err == nil
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			instr.breakerTransitions.Add(context.Background(), 1, metric.WithAttributes(
				attribute.String("name", name),
				attribute.String("from", from.String()),
				attribute.String("to", to.String()),
			))
			// breakerState gauge: 1 when open, 0 otherwise (track as delta).
			openAttr := metric.WithAttributes(attribute.String("name", name))
			switch {
			case to == gobreaker.StateOpen:
				instr.breakerState.Add(context.Background(), 1, openAttr)
			case from == gobreaker.StateOpen:
				instr.breakerState.Add(context.Background(), -1, openAttr)
			}
		},
	}
	return gobreaker.NewCircuitBreaker[*http.Response](st)
}
