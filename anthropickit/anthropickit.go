// Package anthropickit — клиент Claude поверх anthropic-sdk-go: синхронный и
// стриминговый Messages, нормализованный учёт токенов (Usage) и стоимость (Cost)
// по прайс-таблице, предполётный CountTokens. Managed Agents и экзотику — через
// Raw(). Эмитит anthropic_api_calls_total{model,status},
// anthropic_api_latency_seconds{model}, anthropic_api_tokens_total{model,kind},
// anthropic_api_cost_micros_total{model}. Без БД: хранение/леджер/агрегации — на
// стороне потребителя.
package anthropickit

import (
	"context"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

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
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("%w: ANTHROPIC_API_KEY is required", ErrConfig)
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

	ro := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if o.baseURL != "" {
		ro = append(ro, option.WithBaseURL(o.baseURL))
	}
	if cfg.MaxRetries > 0 {
		ro = append(ro, option.WithMaxRetries(cfg.MaxRetries))
	}
	if o.httpClient != nil {
		ro = append(ro, option.WithHTTPClient(o.httpClient))
	}

	return &Client{
		cfg:    cfg,
		sdk:    anthropic.NewClient(ro...),
		log:    log,
		prices: o.prices,
		instr:  newInstruments(),
	}, nil
}

// Stream открывает стриминговый Messages-вызов. Ошибка маппинга запроса —
// через Stream.Err (errStream), сам вызов SSE — лениво в Next.
func (c *Client) Stream(ctx context.Context, req Request) Stream {
	params, model, err := c.toParams(req, defaultMaxTokensStream)
	if err != nil {
		return &errStream{err: err}
	}
	return &sseStream{
		s:      c.sdk.Messages.NewStreaming(ctx, params),
		client: c,
		ctx:    ctx,
		model:  model,
		start:  time.Now(),
	}
}

// Raw — доступ к SDK-клиенту для Managed Agents (Raw().Beta.Agents/Sessions) и
// экзотики, не покрытой нативной поверхностью.
func (c *Client) Raw() anthropic.Client { return c.sdk }

func (c *Client) costOf(model string, u Usage) USDMicros {
	if p, ok := c.prices[model]; ok {
		return p.cost(u)
	}
	return 0
}
