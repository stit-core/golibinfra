// Package httpclient — исходящий HTTP-клиент stit: retry с backoff, circuit
// breaker на upstream-хост, SSE-стримы и OTel-инструментация поверх net/http.
package httpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
)

// Дефолты Config: те же значения, что и в envconfig-тегах, — Validate
// подставляет их вместо нулей.
const (
	defaultTimeout          = 10 * time.Second
	defaultRetryWait        = 200 * time.Millisecond
	defaultRetryMaxWait     = 5 * time.Second
	defaultMaxRedirects     = 10
	defaultSSEMaxEventBytes = 1 << 20
	defaultRecoveryTimeout  = 30 * time.Second
)

const (
	// maxRetryAfter — потолок паузы из заголовка Retry-After сверх RetryMaxWait:
	// защита от переполнения time.Duration на абсурдных значениях сервера.
	maxRetryAfter = 5 * time.Minute
	// maxErrorBody — сколько тела неуспешного ответа попадает в StatusError.
	maxErrorBody = 2048
	drainLimit   = 64 << 10
)

// Config — параметры клиента. Нули Validate заменяет дефолтами, поэтому
// собранный вручную Config{} даёт тот же клиент, что и пустое окружение.
type Config struct {
	Timeout                 time.Duration `envconfig:"HTTPCLIENT_TIMEOUT" default:"10s"`
	MaxRetries              int           `envconfig:"HTTPCLIENT_MAX_RETRIES" default:"3"`
	RetryWait               time.Duration `envconfig:"HTTPCLIENT_RETRY_WAIT" default:"200ms"`
	RetryMaxWait            time.Duration `envconfig:"HTTPCLIENT_RETRY_MAX_WAIT" default:"5s"`
	RetryNonIdempotent      bool          `envconfig:"HTTPCLIENT_RETRY_NON_IDEMPOTENT" default:"false"`
	MaxRedirects            int           `envconfig:"HTTPCLIENT_MAX_REDIRECTS" default:"10"`
	AllowCrossHostRedirects bool          `envconfig:"HTTPCLIENT_ALLOW_CROSS_HOST_REDIRECTS" default:"false"`
	SSEMaxEventBytes        int           `envconfig:"HTTPCLIENT_SSE_MAX_EVENT_BYTES" default:"1048576"`
	Breaker                 BreakerConfig
}

var (
	// ErrConfig — невалидный Config; все ошибки Validate оборачивают его.
	ErrConfig = errors.New("httpclient: invalid config")
	// ErrBreakerOpen — цепь хоста открыта, запрос не отправлен.
	ErrBreakerOpen = errors.New("httpclient: circuit breaker open")
	// ErrTooManyRedirects — цепочка редиректов длиннее Config.MaxRedirects.
	ErrTooManyRedirects = errors.New("httpclient: too many redirects")
	// ErrCrossHostRedirect — редирект на другой хост при AllowCrossHostRedirects=false.
	ErrCrossHostRedirect = errors.New("httpclient: cross-host redirect")

	// errServerStatus — внутренний маркер для breaker'а: 502/503/504 после
	// ретраев считается провалом цепи, но наружу уходит как обычный ответ.
	errServerStatus = errors.New("httpclient: server status")
)

// Validate отвергает отрицательные и противоречивые значения (ErrConfig) и
// подставляет дефолты вместо нулей: клиент без таймаута собрать нельзя.
func (c *Config) Validate() error {
	switch {
	case c.Timeout < 0:
		return fmt.Errorf("%w: Timeout < 0", ErrConfig)
	case c.MaxRetries < 0:
		return fmt.Errorf("%w: MaxRetries < 0", ErrConfig)
	case c.RetryWait < 0:
		return fmt.Errorf("%w: RetryWait < 0", ErrConfig)
	case c.RetryMaxWait < 0:
		return fmt.Errorf("%w: RetryMaxWait < 0", ErrConfig)
	case c.MaxRedirects < 0:
		return fmt.Errorf("%w: MaxRedirects < 0", ErrConfig)
	case c.SSEMaxEventBytes < 0:
		return fmt.Errorf("%w: SSEMaxEventBytes < 0", ErrConfig)
	}
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.RetryWait == 0 {
		c.RetryWait = defaultRetryWait
	}
	if c.RetryMaxWait == 0 {
		c.RetryMaxWait = defaultRetryMaxWait
	}
	if c.MaxRedirects == 0 {
		c.MaxRedirects = defaultMaxRedirects
	}
	if c.SSEMaxEventBytes == 0 {
		c.SSEMaxEventBytes = defaultSSEMaxEventBytes
	}
	if c.RetryMaxWait < c.RetryWait {
		return fmt.Errorf("%w: RetryMaxWait < RetryWait", ErrConfig)
	}
	return c.Breaker.validate()
}

// BeforeAttemptFunc вызывается перед каждой попыткой (attempt с нуля): место для
// per-host rate-limiter'а или дополнительных заголовков. Ошибка прерывает запрос
// и не считается провалом breaker'а.
type BeforeAttemptFunc func(ctx context.Context, attempt int) error

// Option — параметр сборки клиента, не выражаемый через ENV.
type Option func(*Client)

// WithTransport подменяет базовый транспорт (TLS, proxy, тесты). Поверх него
// всё равно ставится otelhttp.Transport.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) { c.base = rt }
}

// WithBeforeAttempt вешает hook перед каждой попыткой (rate-limiter, подпись).
func WithBeforeAttempt(fn BeforeAttemptFunc) Option {
	return func(c *Client) { c.before = fn }
}

// Client — HTTP-клиент на любое число upstream-хостов: breaker и метрики
// разделены по req.URL.Host.
type Client struct {
	cfg      Config
	base     http.RoundTripper
	c        *http.Client
	streamC  *http.Client // SSE: Timeout ограничивает только фазу заголовков (см. Stream)
	breakers *breakerSet
	instr    *instruments
	tracer   trace.Tracer
	before   BeforeAttemptFunc
}

// New собирает клиент: валидирует конфиг (дефолты вместо нулей), применяет
// опции и оборачивает транспорт в otelhttp — спаны попыток без его метрик.
func New(cfg Config, opts ...Option) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, instr: newInstruments(), tracer: otelkit.Tracer("httpclient")}
	for _, o := range opts {
		o(c)
	}
	if c.base == nil {
		c.base = defaultTransport()
	}
	// Без метрик otelhttp: его http.client.request.duration в Prometheus сливается с гистограммой кита.
	transport := otelhttp.NewTransport(&resendTransport{next: c.base},
		otelhttp.WithMeterProvider(metricnoop.NewMeterProvider()))
	c.c = &http.Client{Timeout: cfg.Timeout, Transport: transport, CheckRedirect: c.checkRedirect}
	c.streamC = &http.Client{Transport: transport, CheckRedirect: c.checkRedirect}
	c.breakers = newBreakerSet(cfg.Breaker, c.instr)
	return c, nil
}

// Close закрывает idle-соединения клиента; сам клиент остаётся рабочим —
// следующий запрос откроет соединение заново.
func (c *Client) Close() error {
	if ci, ok := c.base.(interface{ CloseIdleConnections() }); ok {
		ci.CloseIdleConnections()
	}
	return nil
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
	ctx, span := c.startSpan(req, "httpclient.Do")
	defer span.End()
	req = req.WithContext(ctx)

	start := time.Now()
	resp, err := c.breakers.execute(req.URL.Host, func() (*http.Response, error) {
		resp, err := c.doWithRetry(req)
		if err != nil {
			return nil, err
		}
		if breakerFailureStatus(resp.StatusCode) {
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

	c.instr.requestDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("host", req.URL.Host),
			attribute.String("method", req.Method),
			attribute.String("status", statusLabel(resp, err)),
		),
	)
	recordSpanResult(span, resp, err)
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
			timer := time.NewTimer(c.backoff(attempt, lastResp))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, clientSide(fmt.Errorf("httpclient: context done during retry: %w", ctx.Err()))
			case <-timer.C:
			}
			c.instr.retryAttempts.Add(ctx, 1, metric.WithAttributes(attribute.String("host", req.URL.Host)))
			var err error
			if req, err = rewind(req); err != nil {
				return nil, clientSide(err)
			}
		}
		if c.before != nil {
			if err := c.before(ctx, attempt); err != nil {
				return nil, clientSide(fmt.Errorf("httpclient: before attempt %d: %w", attempt, err))
			}
		}

		resp, err := c.c.Do(withResendCount(req, attempt))
		if err != nil {
			// Отмена/дедлайн вызывающего — не провал upstream'а; таймаут одной
			// попытки (Config.Timeout) при живом ctx повторяется.
			if ctx.Err() != nil {
				return nil, clientSide(fmt.Errorf("httpclient: %w", err))
			}
			if permanentErr(err) {
				return nil, fmt.Errorf("httpclient: %w", err)
			}
			lastResp, lastErr = nil, err
			continue
		}
		if !retryableStatus(resp.StatusCode) || attempt == attempts-1 {
			return resp, nil
		}
		drain(resp)
		lastResp, lastErr = resp, nil
	}
	return nil, fmt.Errorf("httpclient: %w", lastErr)
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

// retryableStatus — статусы, которые сервер отдаёт при перегрузке или временном
// отказе. 500 сюда не входит: у большинства upstream'ов он детерминирован.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// breakerFailureStatus — статусы «хост недоступен» для breaker'а: 500/501 от
// живого upstream'а не означают, что цепь пора размыкать.
func breakerFailureStatus(code int) bool {
	switch code {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// clientSideError — провал по вине вызывающего (отмена ctx, hook BeforeAttempt,
// неперематываемое тело): breaker такие ошибки не считает.
type clientSideError struct{ err error }

func (e *clientSideError) Error() string { return e.err.Error() }
func (e *clientSideError) Unwrap() error { return e.err }

func clientSide(err error) error { return &clientSideError{err: err} }

func isClientSide(err error) bool {
	var e *clientSideError
	return errors.As(err, &e)
}

// permanentErr — результат детерминирован (сертификат, схема URL, редиректы):
// повтор даст ту же ошибку и только сожжёт бюджет backoff'а.
func permanentErr(err error) bool {
	if errors.Is(err, ErrTooManyRedirects) || errors.Is(err, ErrCrossHostRedirect) {
		return true
	}
	var (
		certErr   *tls.CertificateVerificationError
		unknownCA x509.UnknownAuthorityError
		hostErr   x509.HostnameError
		invalid   x509.CertificateInvalidError
	)
	if errors.As(err, &certErr) || errors.As(err, &unknownCA) ||
		errors.As(err, &hostErr) || errors.As(err, &invalid) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		// Схему и отсутствие хоста net/http сообщает нетипизированной ошибкой.
		msg := urlErr.Err.Error()
		return strings.Contains(msg, "unsupported protocol scheme") ||
			strings.Contains(msg, "no Host in request URL")
	}
	return false
}

// backoff — Retry-After сервера, иначе RetryWait·2^(attempt-1) + jitter, всё
// под cap RetryMaxWait.
func (c *Client) backoff(attempt int, lastResp *http.Response) time.Duration {
	maxWait := c.cfg.RetryMaxWait
	if lastResp != nil {
		if d, ok := retryAfter(lastResp.Header.Get("Retry-After")); ok {
			return min(d, maxWait)
		}
	}
	base := c.cfg.RetryWait
	exp := maxWait
	// base<<shift переполняется на больших attempt: сдвигаем, только пока
	// результат заведомо не выше cap'а.
	if shift := attempt - 1; shift >= 0 && shift < 62 && base <= maxWait>>shift {
		exp = base << shift
	}
	jitter := rand.N(base) //nolint:gosec // non-crypto jitter for retry backoff
	if exp > maxWait-jitter {
		return maxWait
	}
	return exp + jitter
}

func retryAfter(v string) (time.Duration, bool) {
	d, ok := ParseRetryAfter(v, time.Now())
	if !ok {
		return 0, false
	}
	return min(d, maxRetryAfter), true
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
	_ = resp.Body.Close()
}

// statusLabel — значение label'а status: код ответа, breaker_open при
// отказе цепи, error для транспортных ошибок и отмены ctx.
func statusLabel(resp *http.Response, err error) string {
	switch {
	case resp != nil:
		return strconv.Itoa(resp.StatusCode)
	case errors.Is(err, ErrBreakerOpen):
		return "breaker_open"
	}
	return "error"
}

func (c *Client) startSpan(req *http.Request, name string) (context.Context, trace.Span) {
	return c.tracer.Start(req.Context(), name+" "+req.Method,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			semconv.HTTPRequestMethodKey.String(req.Method),
			semconv.ServerAddress(req.URL.Hostname()),
			semconv.URLFull(redactedURL(req.URL)),
		),
	)
}

func recordSpanResult(span trace.Span, resp *http.Response, err error) {
	if resp != nil {
		span.SetAttributes(semconv.HTTPResponseStatusCode(resp.StatusCode))
	}
	switch {
	case err != nil:
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	case resp != nil && resp.StatusCode >= http.StatusBadRequest:
		span.SetStatus(codes.Error, http.StatusText(resp.StatusCode))
	}
}

// redactedURL — url.full без query и user-info: секрет в query не должен
// уезжать в трейс.
func redactedURL(u *url.URL) string {
	clean := *u
	clean.User, clean.RawQuery, clean.ForceQuery, clean.Fragment, clean.RawFragment = nil, "", false, "", ""
	return clean.String()
}
