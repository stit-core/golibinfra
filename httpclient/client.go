package httpclient

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"git.stit.tech/akadmin/golibinfra/httpkit"
)

type Config struct {
	Timeout    time.Duration `envconfig:"HTTPCLIENT_TIMEOUT" default:"10s"`
	MaxRetries int           `envconfig:"HTTPCLIENT_MAX_RETRIES" default:"3"`
	RetryWait  time.Duration `envconfig:"HTTPCLIENT_RETRY_WAIT" default:"200ms"`
}

type Client struct {
	cfg Config
	c   *http.Client
}

func New(cfg Config) *Client {
	transport := otelhttp.NewTransport(http.DefaultTransport)
	return &Client{
		cfg: cfg,
		c: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
	}
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if id := httpkit.RequestIDFrom(req.Context()); id != "" {
		req.Header.Set(httpkit.HeaderRequestID, id)
	}

	var (
		resp *http.Response
		err  error
	)
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
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
	return resp, fmt.Errorf("httpclient: server error after %d retries", c.cfg.MaxRetries)
}

func backoff(base time.Duration, attempt int) time.Duration {
	exp := base * (1 << attempt)
	jitter := time.Duration(rand.Int63n(int64(base)))
	return exp + jitter
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
