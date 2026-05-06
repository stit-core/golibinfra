package httpclient

import (
	"net/http"
	"time"

	"github.com/sony/gobreaker/v2"
)

type BreakerConfig struct {
	FailureThreshold int           `envconfig:"HTTPCLIENT_BREAKER_FAILURE_THRESHOLD" default:"5"`
	RecoveryTimeout  time.Duration `envconfig:"HTTPCLIENT_BREAKER_RECOVERY_TIMEOUT" default:"30s"`
	SuccessThreshold int           `envconfig:"HTTPCLIENT_BREAKER_SUCCESS_THRESHOLD" default:"3"`
}

func newBreaker(cfg BreakerConfig) *gobreaker.CircuitBreaker[*http.Response] {
	st := gobreaker.Settings{
		Name:    "httpclient",
		Timeout: cfg.RecoveryTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return int(counts.ConsecutiveFailures) >= cfg.FailureThreshold
		},
		IsSuccessful: func(err error) bool {
			return err == nil
		},
	}
	return gobreaker.NewCircuitBreaker[*http.Response](st)
}
