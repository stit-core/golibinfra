// Package anthropickit — клиент Claude поверх anthropic-sdk-go: синхронный и
// стриминговый Messages, нормализованный учёт токенов (Usage) и стоимость (Cost)
// по прайс-таблице, предполётный CountTokens. Managed Agents и экзотику — через
// Raw(). Эмитит anthropic_api_calls_total{model,op,status,priced},
// anthropic_api_latency_seconds{model,op}, anthropic_api_tokens_total{model,kind},
// anthropic_api_cost_micros_total{model} и спан anthropic.messages с gen_ai.*.
// Без БД: хранение/леджер/агрегации — на стороне потребителя.
package anthropickit

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// API — поверхность клиента для DI и моков у потребителя. Stream возвращает
// итератор; ошибки стрима — через Stream.Err.
type API interface {
	Complete(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request) Stream
	CountTokens(ctx context.Context, req Request) (int64, error)
	DefaultModel() string
	Raw() anthropic.Client
}

// Client — клиент Claude API.
type Client struct {
	cfg    Config
	sdk    anthropic.Client
	log    logkit.Logger
	prices map[string]Price
	instr  *instruments
	tracer trace.Tracer

	unknownMu     sync.Mutex
	unknownModels map[string]struct{}
}

var _ API = (*Client)(nil)

// New валидирует cfg и собирает клиента. nil-логгер заменяется на logkit.Nop().
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
	if err := validateBaseURL(o.baseURL); err != nil {
		return nil, err
	}

	// WithoutEnvironmentDefaults: Config — единственный источник endpoint'а и
	// credentials; иначе SDK сам подхватит из окружения токен, base URL и профили.
	ro := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithBaseURL(o.baseURL),
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(cfg.MaxRetries),
		option.WithRequestTimeout(cfg.Timeout),
		option.WithHTTPClient(tracedHTTPClient(o.httpClient)),
		option.WithMiddleware(retryLogMiddleware(log)),
	}

	return &Client{
		cfg:           cfg,
		sdk:           anthropic.NewClient(ro...),
		log:           log,
		prices:        o.prices,
		instr:         newInstruments(),
		tracer:        otelkit.Tracer("anthropickit"),
		unknownModels: map[string]struct{}{},
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
// события читаются в Next. Ошибка маппинга запроса — через Stream.Err (errStream),
// метрик нет: вызова API не было.
func (c *Client) Stream(ctx context.Context, req Request) Stream {
	params, model, err := c.toParams(req, defaultMaxTokensStream)
	if err != nil {
		return &errStream{err: err}
	}
	ctx, span := c.startSpan(ctx, opStream, model, params.MaxTokens)
	start := time.Now()
	return &sseStream{
		s:      c.sdk.Messages.NewStreaming(ctx, params),
		client: c,
		ctx:    ctx,
		span:   span,
		model:  model,
		start:  start,
	}
}

// DefaultModel — модель из Config, которой уходит запрос с пустым Request.Model.
func (c *Client) DefaultModel() string { return c.cfg.DefaultModel }

// Raw — доступ к SDK-клиенту для Managed Agents (Raw().Beta.Agents/Sessions),
// batches, files и экзотики, не покрытой нативной поверхностью.
func (c *Client) Raw() anthropic.Client { return c.sdk }

func (c *Client) costOf(model string, u Usage) (USDMicros, bool) {
	p, ok := priceFor(c.prices, model)
	if !ok {
		return 0, false
	}
	return p.cost(u), true
}

func (c *Client) priced(model string) bool {
	_, ok := priceFor(c.prices, model)
	return ok
}

// modelLabelOther ограничивает кардинальность label `model`: всё вне
// прайс-таблицы схлопывается в одну серию.
const modelLabelOther = "other"

// unknownModelLogCap ограничивает множество дедупликации для warn-лога ниже.
const unknownModelLogCap = 64

func (c *Client) modelLabel(ctx context.Context, model string) string {
	if c.priced(model) {
		return model
	}
	c.warnUnknownModel(ctx, model)
	return modelLabelOther
}

func (c *Client) warnUnknownModel(ctx context.Context, model string) {
	c.unknownMu.Lock()
	defer c.unknownMu.Unlock()
	if _, seen := c.unknownModels[model]; seen || len(c.unknownModels) >= unknownModelLogCap {
		return
	}
	c.unknownModels[model] = struct{}{}
	c.log.Warn(ctx, "anthropickit: model is absent from the price table, metric label collapsed",
		"model", model, "label", modelLabelOther)
}

// observe — единая точка завершения вызова: метрики + лог сбоя. Накопленный до
// сбоя usage сохраняется: API его тарифицирует.
func (c *Client) observe(ctx context.Context, start time.Time, r callResult, err error) {
	r.dur = time.Since(start)
	if err != nil {
		r.status = statusError
		c.logFailure(ctx, r.op, r.model, err)
	}
	r.model = c.modelLabel(ctx, r.model)
	c.instr.record(ctx, r)
}

func (c *Client) logFailure(ctx context.Context, op, model string, err error) {
	if apiErr, ok := errors.AsType[*anthropic.Error](err); ok {
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
