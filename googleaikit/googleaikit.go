// Package googleaikit — клиент Google Gemini поверх google.golang.org/genai:
// dual-backend (Gemini Developer API / AI-Studio по API-ключу ИЛИ Vertex AI по
// project+location/ADC), все модальности — generateContent (chat/мультимодал/
// thinking, sync+stream), embeddings, images (Nano Banana + Imagen), video (Veo,
// async LRO), audio (TTS). Нормализованный учёт токенов (Usage) и стоимость (Cost)
// по прайс-таблице, предполётный CountTokens. Экзотику (Files/Caches/Live/Batches)
// — через Raw(). Эмитит google_api_calls_total{model,op,status},
// google_api_latency_seconds{model,op}, google_api_tokens_total{model,kind},
// google_api_cost_micros_total{model}. Без БД: хранение/леджер/агрегации usage/cost
// — на стороне потребителя.
package googleaikit

import (
	"context"
	"fmt"

	"google.golang.org/genai"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// API — поверхность клиента для DI и моков у потребителя. Покрывает все модальности
// доменов google/*. Stream возвращает итератор; ошибки стрима — через Stream.Err.
// Video асинхронен: SubmitVideo → operation, PollVideo до готовности.
type API interface {
	GenerateContent(ctx context.Context, req Request) (*Response, error)
	Stream(ctx context.Context, req Request) Stream
	CountTokens(ctx context.Context, req Request) (int64, error)
	EmbedContent(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
	GenerateImages(ctx context.Context, req ImageRequest) (*ImageResponse, error)
	SubmitVideo(ctx context.Context, req VideoRequest) (*VideoOperation, error)
	PollVideo(ctx context.Context, op *VideoOperation) (*VideoOperation, error)
	GenerateSpeech(ctx context.Context, req SpeechRequest) (*SpeechResponse, error)
	Raw() *genai.Client
}

type Client struct {
	cfg    Config
	sdk    *genai.Client
	log    logkit.Logger
	clock  clockkit.Clock
	prices map[string]Price
	instr  *instruments
}

var _ API = (*Client)(nil)

// New строит клиент под выбранный backend. AI-Studio требует APIKey; Vertex —
// Project+Location (креды из ADC или кастомного HTTPClient). clock — для пауз
// поллинга LRO (Veo); nil → clockkit.Real.
func New(cfg Config, log logkit.Logger, opts ...Option) (*Client, error) {
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

	cc, err := clientConfig(cfg, o)
	if err != nil {
		return nil, err
	}
	sdk, err := genai.NewClient(context.Background(), cc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}

	return &Client{
		cfg:    cfg,
		sdk:    sdk,
		log:    log,
		clock:  clockkit.Real{},
		prices: o.prices,
		instr:  newInstruments(),
	}, nil
}

// clientConfig нормализует dual-backend различия в genai.ClientConfig.
func clientConfig(cfg Config, o options) (*genai.ClientConfig, error) {
	cc := &genai.ClientConfig{
		HTTPClient: o.httpClient,
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    o.baseURL,
			APIVersion: cfg.APIVersion,
		},
	}
	if cfg.isVertex() {
		if cfg.Project == "" || cfg.Location == "" {
			return nil, fmt.Errorf("%w: GOOGLE_PROJECT and GOOGLE_LOCATION are required for vertex backend", ErrConfig)
		}
		cc.Backend = genai.BackendVertexAI
		cc.Project = cfg.Project
		cc.Location = cfg.Location
		return cc, nil
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("%w: GOOGLE_API_KEY is required for aistudio backend", ErrConfig)
	}
	cc.Backend = genai.BackendGeminiAPI
	cc.APIKey = cfg.APIKey
	return cc, nil
}

// Raw — доступ к SDK-клиенту для экзотики (Files/Caches/Live/Batches/Tunings),
// не покрытой нативной поверхностью, и для NativePassthrough.
func (c *Client) Raw() *genai.Client { return c.sdk }

func (c *Client) modelOr(model string) string {
	if model != "" {
		return model
	}
	return c.cfg.DefaultModel
}

func (c *Client) costOf(model string, u Usage) USDMicros {
	if p, ok := c.prices[model]; ok {
		return p.cost(u)
	}
	return 0
}
