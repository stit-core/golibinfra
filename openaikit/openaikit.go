// Package openaikit — клиент OpenAI поверх openai-go/v3: chat (Responses +
// Chat Completions, sync и streaming), embeddings, image generation, audio
// (STT/TTS), moderation. Нормализованный учёт токенов (Usage по контракту
// accounting-ports) и стоимость (Cost) по прайс-таблице. Экзотику (batch, files,
// vector stores, realtime control-plane, undocumented params) — через Raw().
// Base URL/org/project/auth-scheme инжектируемы (Azure-готовность). Эмитит
// openai_api_calls_total{model,modality,status}, openai_api_latency_seconds
// {model,modality}, openai_api_tokens_total{model,kind},
// openai_api_cost_micros_total{model}. Без БД: хранение/леджер/агрегации — на
// стороне потребителя.
package openaikit

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// API — поверхность клиента для DI и моков у потребителя. Объединяет модальности;
// Stream*-методы возвращают итератор, ошибки которого — через Stream.Err.
type API interface {
	// Responses API (рекомендуемый путь).
	Respond(ctx context.Context, req ResponsesRequest) (*ResponsesResponse, error)
	StreamResponse(ctx context.Context, req ResponsesRequest) Stream

	// Chat Completions.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	StreamChat(ctx context.Context, req ChatRequest) Stream

	// Прочие модальности.
	Embed(ctx context.Context, req EmbeddingsRequest) (*EmbeddingsResponse, error)
	GenerateImage(ctx context.Context, req ImageRequest) (*ImageResponse, error)
	Transcribe(ctx context.Context, req TranscribeRequest) (*TranscribeResponse, error)
	Speak(ctx context.Context, req SpeechRequest) (*SpeechResponse, error)
	Moderate(ctx context.Context, req ModerationRequest) (*ModerationResponse, error)

	// Raw — SDK-клиент для непокрытой нативно поверхности (batch, files,
	// vector stores, realtime control-plane, и т.п.).
	Raw() openai.Client
}

type Client struct {
	cfg    Config
	sdk    openai.Client
	log    logkit.Logger
	prices map[string]Price
	instr  *instruments
}

var _ API = (*Client)(nil)

func New(cfg Config, log logkit.Logger, opts ...Option) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("%w: OPENAI_API_KEY is required", ErrConfig)
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
	if cfg.Organization != "" {
		ro = append(ro, option.WithOrganization(cfg.Organization))
	}
	if cfg.Project != "" {
		ro = append(ro, option.WithProject(cfg.Project))
	}
	if cfg.MaxRetries > 0 {
		ro = append(ro, option.WithMaxRetries(cfg.MaxRetries))
	}
	// SDK не выставляет дефолт-таймаут — иначе зависший апстрим течёт. RequestTimeout
	// бьёт по каждой попытке (меньше суммарного ctx-deadline на все ретраи).
	if cfg.Timeout > 0 {
		ro = append(ro, option.WithRequestTimeout(cfg.Timeout))
	}
	if o.httpClient != nil {
		ro = append(ro, option.WithHTTPClient(o.httpClient))
	}
	ro = append(ro, o.extra...)

	return &Client{
		cfg:    cfg,
		sdk:    openai.NewClient(ro...),
		log:    log,
		prices: o.prices,
		instr:  newInstruments(),
	}, nil
}

// Raw — доступ к SDK-клиенту для поверхности, не покрытой нативно.
func (c *Client) Raw() openai.Client { return c.sdk }

func (c *Client) costOf(model string, u Usage) USDMicros {
	if p, ok := c.prices[model]; ok {
		return p.cost(u)
	}
	return 0
}

func (c *Client) modelOr(model string) string {
	if model == "" {
		return c.cfg.DefaultModel
	}
	return model
}
