package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// BreakerConfig — политика circuit breaker'а на upstream-хост.
type BreakerConfig struct {
	// FailureThreshold = 0 выключает breaker.
	FailureThreshold int           `envconfig:"HTTPCLIENT_BREAKER_FAILURE_THRESHOLD" default:"5"`
	RecoveryTimeout  time.Duration `envconfig:"HTTPCLIENT_BREAKER_RECOVERY_TIMEOUT" default:"30s"`
}

func (c *BreakerConfig) validate() error {
	if c.FailureThreshold < 0 {
		return fmt.Errorf("%w: Breaker.FailureThreshold < 0", ErrConfig)
	}
	if c.RecoveryTimeout < 0 {
		return fmt.Errorf("%w: Breaker.RecoveryTimeout < 0", ErrConfig)
	}
	if c.RecoveryTimeout == 0 {
		c.RecoveryTimeout = defaultRecoveryTimeout
	}
	return nil
}

// breakerSet — breaker на каждый upstream-хост: отказ одного хоста не размыкает
// цепь для остальных.
type breakerSet struct {
	cfg   BreakerConfig
	instr *instruments

	mu sync.Mutex
	m  map[string]*gobreaker.CircuitBreaker[*http.Response]
}

func newBreakerSet(cfg BreakerConfig, instr *instruments) *breakerSet {
	return &breakerSet{cfg: cfg, instr: instr, m: map[string]*gobreaker.CircuitBreaker[*http.Response]{}}
}

func (s *breakerSet) execute(host string, fn func() (*http.Response, error)) (*http.Response, error) {
	if s.cfg.FailureThreshold == 0 {
		return fn()
	}
	return s.get(host).Execute(fn)
}

func (s *breakerSet) get(host string) *gobreaker.CircuitBreaker[*http.Response] {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cb, ok := s.m[host]; ok {
		return cb
	}
	//nolint:bodyclose // тип-параметр *http.Response, ответ здесь не создаётся
	cb := gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
		Name:    host,
		Timeout: s.cfg.RecoveryTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return int(counts.ConsecutiveFailures) >= s.cfg.FailureThreshold
		},
		IsSuccessful: func(err error) bool { return err == nil },
		// Отмена ctx вызывающего и отказ hook'а BeforeAttempt — не сигнал о
		// здоровье upstream'а: всплеск barge-in не должен отключать хост.
		IsExcluded: isClientSide,
		OnStateChange: func(name string, from, to gobreaker.State) {
			ctx := context.Background()
			s.instr.breakerTransitions.Add(ctx, 1, metric.WithAttributes(
				attribute.String("host", name),
				attribute.String("from", from.String()),
				attribute.String("to", to.String()),
			))
			hostAttr := metric.WithAttributes(attribute.String("host", name))
			switch {
			case to == gobreaker.StateOpen:
				s.instr.breakerOpen.Add(ctx, 1, hostAttr)
			case from == gobreaker.StateOpen:
				s.instr.breakerOpen.Add(ctx, -1, hostAttr)
			}
		},
	})
	s.m[host] = cb
	return cb
}
