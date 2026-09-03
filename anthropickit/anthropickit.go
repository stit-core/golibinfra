// Package anthropickit — клиент Claude поверх anthropic-sdk-go: синхронный и
// стриминговый Messages, нормализованный учёт токенов (Usage) и стоимость (Cost)
// по прайс-таблице, предполётный CountTokens. Managed Agents и экзотику — через
// Raw(). Эмитит anthropic_api_calls_total{model,op,status,priced},
// anthropic_api_latency_seconds{model,op}, anthropic_api_tokens_total{model,kind},
// anthropic_api_cost_micros_total{model}; клиентские HTTP-спаны — через
// otelhttp-транспорт. Без БД: хранение/леджер/агрегации — на стороне потребителя.
package anthropickit

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// API — поверхность клиента для DI и моков у потребителя. Stream возвращает
// итератор; ошибки стрима — через Stream.Err.
type API interface {
	Complete(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request) Stream
	CountTokens(ctx context.Context, req Request) (int64, error)
	Raw() anthropic.Client
}

type Client struct {
	cfg    Config
	sdk    anthropic.Client
	log    logkit.Logger
	prices map[string]Price
	instr  *instruments
}

var _ API = (*Client)(nil)

func New(cfg Config, log logkit.Logger, opts ...Option) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = logkit.Nop()
	}

	o := options{prices: defaultPrices, baseURL: cfg.BaseURL}
	for _, fn := range opts {
		fn(&o)
	}
	if o.prices == nil {
		o.prices = defaultPrices
	}

	ro := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(cfg.MaxRetries),
		option.WithHTTPClient(tracedHTTPClient(o.httpClient)),
		option.WithMiddleware(retryLogMiddleware(log)),
	}
	if o.baseURL != "" {
		ro = append(ro, option.WithBaseURL(o.baseURL))
	}
	if cfg.Timeout > 0 {
		ro = append(ro, option.WithRequestTimeout(cfg.Timeout))
	}

	return &Client{
		cfg:    cfg,
		sdk:    anthropic.NewClient(ro...),
		log:    log,
		prices: o.prices,
		instr:  newInstruments(),
	}, nil
}

// responseHeaderTimeout повторяет дефолт транспорта SDK: молчащий после
// установки соединения сервер не должен держать запрос вечно; тело стрима не
// ограничивает.
const responseHeaderTimeout = 10 * time.Minute

// tracedHTTPClient оборачивает транспорт в otelhttp — SDK сам спанов не ведёт.
func tracedHTTPClient(base *http.Client) *http.Client {
	hc := http.Client{}
	if base != nil {
		hc = *base
	}
	rt := hc.Transport
	if rt == nil {
		rt = defaultTransport()
	}
	hc.Transport = otelhttp.NewTransport(rt)
	return &hc
}

func defaultTransport() http.RoundTripper {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t = t.Clone()
		t.ResponseHeaderTimeout = responseHeaderTimeout
		return t
	}
	return http.DefaultTransport
}

// retryCountHeader — SDK нумерует попытки в заголовке; middleware видит каждую.
const retryCountHeader = "X-Stainless-Retry-Count"

func retryLogMiddleware(log logkit.Logger) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		if n := req.Header.Get(retryCountHeader); n != "" && n != "0" {
			log.Debug(req.Context(), "anthropickit: retrying request",
				"attempt", n, "method", req.Method, "path", req.URL.Path)
		}
		return next(req)
	}
}

// Stream открывает стриминговый Messages-вызов: HTTP-запрос уходит здесь же,
// события читаются в Next. Ошибка маппинга запроса — через Stream.Err (errStream).
func (c *Client) Stream(ctx context.Context, req Request) Stream {
	params, model, err := c.toParams(req, defaultMaxTokensStream)
	if err != nil {
		return &errStream{err: err}
	}
	start := time.Now()
	return &sseStream{
		s:      c.sdk.Messages.NewStreaming(ctx, params),
		client: c,
		ctx:    ctx,
		model:  model,
		start:  start,
	}
}

// Raw — доступ к SDK-клиенту для Managed Agents (Raw().Beta.Agents/Sessions) и
// экзотики, не покрытой нативной поверхностью.
func (c *Client) Raw() anthropic.Client { return c.sdk }

func (c *Client) costOf(model string, u Usage) (USDMicros, bool) {
	p, ok := priceFor(c.prices, model)
	if !ok {
		return 0, false
	}
	return p.cost(u), true
}

// observe — единая точка завершения вызова: метрики + лог сбоя. При ошибке
// status=error и usage/cost не учитываются.
func (c *Client) observe(ctx context.Context, start time.Time, r callResult, err error) {
	r.dur = time.Since(start)
	_, r.priced = priceFor(c.prices, r.model)
	if err != nil {
		r.status, r.usage, r.cost = statusError, Usage{}, 0
		c.logFailure(ctx, r.op, r.model, err)
	}
	c.instr.record(ctx, r)
}

func (c *Client) logFailure(ctx context.Context, op, model string, err error) {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		kv := []any{
			"op", op, "model", model, "status", apiErr.StatusCode,
			"type", string(apiErr.Type()), "request_id", apiErr.RequestID,
		}
		if apiErr.Response != nil {
			if ra := apiErr.Response.Header.Get("Retry-After"); ra != "" {
				kv = append(kv, "retry_after", ra)
			}
		}
		c.log.Warn(ctx, "anthropickit: api error", kv...)
		return
	}
	if errors.Is(err, context.Canceled) {
		c.log.Debug(ctx, "anthropickit: request canceled", "op", op, "model", model)
		return
	}
	c.log.Warn(ctx, "anthropickit: request failed", "op", op, "model", model, "error", err.Error())
}
