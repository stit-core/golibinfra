// Package httpclient — outbound HTTP-клиент с retry, circuit breaker на хост
// (sony/gobreaker/v2), OTel-спанами через otelhttp.Transport и прокидыванием
// X-Request-ID из ctx. Эмитит http_client_request_duration_seconds,
// http_client_retry_attempts_total, http_client_breaker_transitions_total,
// http_client_breaker_open.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/requestid"
)

type Config struct {
	Timeout            time.Duration `envconfig:"HTTPCLIENT_TIMEOUT" default:"10s"`
	MaxRetries         int           `envconfig:"HTTPCLIENT_MAX_RETRIES" default:"3"`
	RetryWait          time.Duration `envconfig:"HTTPCLIENT_RETRY_WAIT" default:"200ms"`
	RetryMaxWait       time.Duration `envconfig:"HTTPCLIENT_RETRY_MAX_WAIT" default:"5s"`
	RetryNonIdempotent bool          `envconfig:"HTTPCLIENT_RETRY_NON_IDEMPOTENT" default:"false"`
	Breaker            BreakerConfig
}

var (
	ErrConfig      = errors.New("httpclient: invalid config")
	ErrBreakerOpen = errors.New("httpclient: circuit breaker open")

	// errServerStatus — внутренний маркер для breaker'а: 5xx после ретраев
	// считается провалом цепи, но наружу уходит как обычный ответ.
	errServerStatus = errors.New("httpclient: server status")
)

func (c Config) Validate() error {
	switch {
	case c.Timeout < 0:
		return fmt.Errorf("%w: Timeout < 0", ErrConfig)
	case c.MaxRetries < 0:
		return fmt.Errorf("%w: MaxRetries < 0", ErrConfig)
	case c.MaxRetries > 0 && c.RetryWait <= 0:
		return fmt.Errorf("%w: RetryWait must be > 0 when MaxRetries > 0", ErrConfig)
	case c.RetryMaxWait > 0 && c.RetryMaxWait < c.RetryWait:
		return fmt.Errorf("%w: RetryMaxWait < RetryWait", ErrConfig)
	}
	return c.Breaker.validate()
}

// BeforeAttemptFunc вызывается перед каждой попыткой (attempt с нуля): место для
// per-host rate-limiter'а или дополнительных заголовков. Ошибка прерывает запрос.
type BeforeAttemptFunc func(ctx context.Context, attempt int) error

type Option func(*Client)

// WithTransport подменяет базовый транспорт (TLS, proxy, тесты). Поверх него
// всё равно ставится otelhttp.Transport.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) { c.base = rt }
}

func WithBeforeAttempt(fn BeforeAttemptFunc) Option {
	return func(c *Client) { c.before = fn }
}

type Client struct {
	cfg      Config
	base     http.RoundTripper
	c        *http.Client
	streamC  *http.Client // для SSE: без Timeout (живёт пока жив ctx), без retry и breaker
	breakers *breakerSet
	instr    *instruments
	before   BeforeAttemptFunc
}

func New(cfg Config, opts ...Option) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, base: http.DefaultTransport, instr: newInstruments()}
	for _, o := range opts {
		o(c)
	}
	transport := otelhttp.NewTransport(c.base)
	c.c = &http.Client{Timeout: cfg.Timeout, Transport: transport}
	c.streamC = &http.Client{Transport: transport}
	c.breakers = newBreakerSet(cfg.Breaker, c.instr)
	return c, nil
}

type allowRetryKey struct{}

// AllowRetry помечает запрос как безопасный для повтора независимо от метода:
// для POST/PATCH, идемпотентность которых обеспечена на стороне сервера.
func AllowRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, allowRetryKey{}, true)
}

func retryAllowed(ctx context.Context) bool {
	v, _ := ctx.Value(allowRetryKey{}).(bool)
	return v
}

// Do выполняет запрос с retry и breaker'ом хоста. Ответ с любым статусом,
// включая 5xx после исчерпания ретраев, возвращается как ответ (err == nil);
// ошибка — только транспорт, ctx или открытый breaker (ErrBreakerOpen).
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if id := requestid.From(req.Context()); id != "" && req.Header.Get(requestid.Header) == "" {
		req.Header.Set(requestid.Header, id)
	}

	start := time.Now()
	resp, err := c.breakers.execute(req.URL.Host, func() (*http.Response, error) {
		resp, err := c.doWithRetry(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			return resp, errServerStatus
		}
		return resp, nil
	})
	switch {
	case errors.Is(err, errServerStatus):
		err = nil
	case errors.Is(err, gobreaker.ErrOpenState), errors.Is(err, gobreaker.ErrTooManyRequests):
		err = fmt.Errorf("%w: %w", ErrBreakerOpen, err)
	}

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
	ctx := req.Context()
	attempts := 1
	if c.retryable(req) {
		attempts += c.cfg.MaxRetries
	}

	var (
		lastResp *http.Response
		lastErr  error
	)
	for attempt := range attempts {
		if attempt > 0 {
			c.instr.retryAttempts.Add(ctx, 1, metric.WithAttributes(attribute.String("host", req.URL.Host)))
			timer := time.NewTimer(c.backoff(attempt, lastResp))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("httpclient: context done during retry: %w", ctx.Err())
			case <-timer.C:
			}
			var err error
			if req, err = rewind(req); err != nil {
				return nil, err
			}
		}
		if c.before != nil {
			if err := c.before(ctx, attempt); err != nil {
				return nil, fmt.Errorf("httpclient: before attempt %d: %w", attempt, err)
			}
		}

		resp, err := c.c.Do(req)
		if err != nil {
			if isContextErr(err) {
				return nil, fmt.Errorf("httpclient: %w", err)
			}
			lastErr, lastResp = err, nil
			continue
		}
		if !retryableStatus(resp.StatusCode) || attempt == attempts-1 {
			return resp, nil
		}
		drain(resp)
		lastResp, lastErr = resp, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("httpclient: %w", lastErr)
	}
	return lastResp, nil
}

// retryable — повтор безопасен, если метод идемпотентен (или запрос явно помечен)
// и тело можно перечитать через GetBody.
func (c *Client) retryable(req *http.Request) bool {
	if c.cfg.MaxRetries == 0 {
		return false
	}
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return false
	}
	if c.cfg.RetryNonIdempotent || retryAllowed(req.Context()) || req.Header.Get("Idempotency-Key") != "" {
		return true
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete, http.MethodTrace:
		return true
	}
	return false
}

func rewind(req *http.Request) (*http.Request, error) {
	clone := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody || req.GetBody == nil {
		return clone, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("httpclient: rewind body: %w", err)
	}
	clone.Body = body
	return clone, nil
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// backoff — Retry-After сервера, иначе RetryWait·2^(attempt-1) + jitter, всё
// под cap RetryMaxWait.
func (c *Client) backoff(attempt int, lastResp *http.Response) time.Duration {
	maxWait := c.cfg.RetryMaxWait
	if maxWait <= 0 {
		maxWait = time.Duration(1<<62 - 1)
	}
	if lastResp != nil {
		if d, ok := retryAfter(lastResp.Header.Get("Retry-After")); ok {
			return min(d, maxWait)
		}
	}
	base := c.cfg.RetryWait
	exp := maxWait
	if shift := attempt - 1; shift < 30 {
		exp = min(base<<shift, maxWait)
	}
	jitter := rand.N(base) //nolint:gosec // non-crypto jitter for retry backoff
	return min(exp+jitter, maxWait)
}

func retryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
