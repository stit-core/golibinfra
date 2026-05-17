// Package httpclient — outbound HTTP-клиент с retry, circuit breaker
// (sony/gobreaker/v2), OTel-инструментированием через otelhttp.Transport
// и автоматическим прокидыванием X-Request-ID из ctx. Эмитит
// http_client_request_duration_seconds, http_client_breaker_transitions_total,
// http_client_breaker_open, http_client_retry_attempts_total.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-infra/golibinfra/httpkit"
)

type Config struct {
	Timeout    time.Duration `envconfig:"HTTPCLIENT_TIMEOUT" default:"10s"`
	MaxRetries int           `envconfig:"HTTPCLIENT_MAX_RETRIES" default:"3"`
	RetryWait  time.Duration `envconfig:"HTTPCLIENT_RETRY_WAIT" default:"200ms"`
	Breaker    BreakerConfig
}

type Client struct {
	cfg     Config
	c       *http.Client
	breaker *gobreaker.CircuitBreaker[*http.Response]
	instr   *httpClientInstruments
}

func New(cfg Config) *Client {
	transport := otelhttp.NewTransport(http.DefaultTransport)
	instr := newInstruments()
	return &Client{
		cfg: cfg,
		c: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		breaker: newBreaker(cfg.Breaker, instr),
		instr:   instr,
	}
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if id := httpkit.RequestIDFrom(req.Context()); id != "" {
		req.Header.Set(httpkit.HeaderRequestID, id)
	}

	start := time.Now()
	resp, err := c.breaker.Execute(func() (*http.Response, error) {
		return c.doWithRetry(req)
	})

	status := "error"
	if resp != nil {
		status = strconv.Itoa(resp.StatusCode)
	}
	c.instr.requestDuration.Record(req.Context(), time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("host", req.URL.Host),
			attribute.String("method", req.Method),
			attribute.String("status", status),
		),
	)
	return resp, err
}

func (c *Client) doWithRetry(req *http.Request) (*http.Response, error) {
	var (
		resp *http.Response
		err  error
	)
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			c.instr.retryAttempts.Add(req.Context(), 1, metric.WithAttributes(
				attribute.String("host", req.URL.Host),
			))
			wait := backoff(c.cfg.RetryWait, attempt)
			select {
			case <-req.Context().Done():
				return nil, fmt.Errorf("httpclient: context cancelled during retry: %w", req.Context().Err())
			case <-time.After(wait):
			}
			cloned := req.Clone(req.Context())
			req = cloned
		}
		resp, err = c.c.Do(req)
		if err != nil {
			if isContextErr(err) {
				return nil, err
			}
			continue
		}
		if resp.StatusCode < 500 {
			return resp, nil
		}
		resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("httpclient: %w", err)
	}
	return nil, fmt.Errorf("httpclient: server error after %d retries", c.cfg.MaxRetries)
}

func backoff(base time.Duration, attempt int) time.Duration {
	exp := base * (1 << attempt)
	jitter := time.Duration(rand.Int63n(int64(base))) //nolint:gosec // non-crypto jitter for retry backoff
	return exp + jitter
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
