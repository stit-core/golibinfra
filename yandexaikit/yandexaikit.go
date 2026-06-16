// Package yandexaikit — клиент Yandex Cloud Foundation Models (AI Studio) поверх
// raw REST (golibinfra httpclient, без SDK): синхронный и стриминговый (cumulative)
// completion, async completion (LRO), text embeddings, YandexART image generation
// (LRO), нативный passthrough. Нормализованный учёт токенов (Usage) и стоимость
// (Cost в микро-рублях) по прайс-таблице. Две схемы auth (Api-Key / IAM-JWT с
// refresh) — в auth.go. Эмитит yandex_api_calls_total{endpoint,model,status},
// yandex_api_latency_seconds{endpoint,model}, yandex_api_tokens_total{model,kind},
// yandex_api_cost_micros_total{model}. Без БД: хранение/леджер/агрегации — на
// стороне потребителя.
package yandexaikit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// API — поверхность клиента для DI и моков у потребителя. Async-методы submit
// возвращают operation-id; Await* поллит до done. Stream.Err несёт ошибку стрима.
type API interface {
	// Completion — синхронный completion (sync-квота = concurrency 10).
	Completion(ctx context.Context, req Request) (*Response, error)
	// Stream — стриминговый completion (cumulative chunks; FINAL — в Final).
	Stream(ctx context.Context, req Request) Stream
	// CompletionAsync — submit отложенного completion, возвращает operation-id.
	CompletionAsync(ctx context.Context, req Request) (string, error)
	// AwaitCompletion поллит operation completionAsync до done и строит Response.
	AwaitCompletion(ctx context.Context, operationID string) (*Response, error)
	// TextEmbedding — синхронная векторизация.
	TextEmbedding(ctx context.Context, in EmbeddingInput) (*EmbeddingResult, error)
	// ImageGenerationAsync — submit YandexART, возвращает operation-id.
	ImageGenerationAsync(ctx context.Context, in ImageInput) (string, error)
	// AwaitImage поллит operation imageGenerationAsync до done и декодит кадр.
	AwaitImage(ctx context.Context, operationID string) (*ImageResult, error)
	// PollOperation — один опрос операции (для пер-поллинга на стороне gateway).
	PollOperation(ctx context.Context, operationID string) (*Operation, error)
	// Raw — нативный passthrough одного REST-вызова под auth кита (NativePassthrough).
	Raw(ctx context.Context, method, path string, body []byte) (*http.Response, error)
}

type Client struct {
	cfg         Config
	hc          *httpclient.Client
	clock       clockkit.Clock
	log         logkit.Logger
	auth        authProvider
	prices      map[string]Price
	pricesAsync map[string]Price
	poll        pollConfig
	instr       *instruments
}

var _ API = (*Client)(nil)

// New собирает Client поверх httpclient (retry/breaker/OTel), clockkit и logkit.
func New(cfg Config, hc *httpclient.Client, clock clockkit.Clock, log logkit.Logger, opts ...Option) (*Client, error) {
	if hc == nil {
		return nil, fmt.Errorf("%w: httpclient is required", ErrConfig)
	}
	if clock == nil {
		clock = clockkit.Real{}
	}
	if log == nil {
		log = logkit.Nop()
	}

	auth, err := newAuth(cfg, hc, clock)
	if err != nil {
		return nil, err
	}

	o := options{prices: defaultPrices, pricesAsync: defaultPricesAsync, poll: defaultPollConfig}
	for _, fn := range opts {
		fn(&o)
	}
	if o.prices == nil {
		o.prices = defaultPrices
	}
	if o.pricesAsync == nil {
		o.pricesAsync = defaultPricesAsync
	}
	if o.baseURL != "" {
		cfg.BaseURL = o.baseURL
	}

	return &Client{
		cfg:         cfg,
		hc:          hc,
		clock:       clock,
		log:         log,
		auth:        auth,
		prices:      o.prices,
		pricesAsync: o.pricesAsync,
		poll:        o.poll,
		instr:       newInstruments(),
	}, nil
}

// modelURI собирает полный modelUri из bare model и схемы. Если model уже несёт
// схему ("gpt://…"), возвращает как есть. folder подставляется для IAM-схемы; на
// Api-Key folder в Config пуст — modelUri требует явной формы со схемой.
func (c *Client) modelURI(scheme, model string) string {
	if indexScheme(model) >= 0 {
		return model // уже полный modelUri
	}
	return scheme + "://" + c.cfg.FolderID + "/" + model
}

// costOf — sync-стоимость по таблице кита.
func (c *Client) costOf(model string, u Usage) RUBMicros {
	if p, ok := c.prices[modelKey(model)]; ok {
		return p.cost(u)
	}
	return 0
}

// costOfAsync — async-стоимость по таблице кита.
func (c *Client) costOfAsync(model string, u Usage) RUBMicros {
	if p, ok := c.pricesAsync[modelKey(model)]; ok {
		return p.cost(u)
	}
	return 0
}

// jsonRequest — параметры одного REST-вызова с JSON-телом и декодом ответа.
type jsonRequest struct {
	method   string
	url      string // полный URL (host+path)
	body     any    // маршалится в JSON, если не nil
	endpoint string // метка метрики, напр. "completion"
	model    string // для метрик/cost (bare-id или modelUri)
}

// doJSON выполняет аутентифицированный REST-вызов, декодит JSON в out (если не nil)
// и пишет calls/latency-метрики. Токены/cost метрики — на вызывающем (он знает usage).
func (c *Client) doJSON(ctx context.Context, r jsonRequest, out any) error {
	var reader io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return fmt.Errorf("yandexaikit: %s: marshal request: %w", r.endpoint, err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, r.method, r.url, reader)
	if err != nil {
		return fmt.Errorf("yandexaikit: %s: new request: %w", r.endpoint, err)
	}
	c.setHeaders(req, r.body != nil)
	if err := c.auth.apply(ctx, req); err != nil {
		return fmt.Errorf("yandexaikit: %s: auth: %w", r.endpoint, err)
	}

	start := c.clock.Now()
	resp, err := c.hc.Do(req)
	dur := c.clock.Now().Sub(start)

	status := "error"
	if resp != nil {
		status = strconv.Itoa(resp.StatusCode)
	}
	c.recordCall(ctx, r.endpoint, r.model, status, dur)

	if err != nil {
		return fmt.Errorf("yandexaikit: %s: %w", r.endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return classify(resp.StatusCode, parseAPIError(resp))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("yandexaikit: %s: read body: %w", r.endpoint, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("yandexaikit: %s: decode: %w", r.endpoint, err)
	}
	return nil
}

// setHeaders ставит Content-Type (если есть тело) и x-data-logging-enabled.
// Authorization/x-folder-id — на authProvider (в doJSON через c.auth.apply).
func (c *Client) setHeaders(req *http.Request, hasBody bool) {
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-data-logging-enabled", strconv.FormatBool(c.cfg.DataLogging))
}

// recordCall пишет calls+latency без токенов/cost (метрики токенов — на успешных
// путях с известным usage, через instr.recordUsage).
func (c *Client) recordCall(ctx context.Context, endpoint, model, status string, dur time.Duration) {
	c.instr.calls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("endpoint", endpoint),
		attribute.String("model", model),
		attribute.String("status", status),
	))
	c.instr.latency.Record(ctx, dur.Seconds(), metric.WithAttributes(
		attribute.String("endpoint", endpoint),
		attribute.String("model", model),
	))
}

// encodeJSON маршалит v в io.Reader для тела запроса.
func encodeJSON(v any) (io.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("yandexaikit: marshal: %w", err)
	}
	return bytes.NewReader(b), nil
}

// Raw — нативный passthrough одного REST-вызова под auth кита (NativePassthrough).
// path — относительный путь под BaseURL (напр. "/foundationModels/v1/completion").
// body — нативное тело как есть (nil для GET). Возвращает *http.Response: учёт и
// разбор тела (включая стрим) — на стороне gateway. Закрыть resp.Body — на вызывающем.
func (c *Client) Raw(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("yandexaikit: raw: new request: %w", err)
	}
	c.setHeaders(req, body != nil)
	if err := c.auth.apply(ctx, req); err != nil {
		return nil, fmt.Errorf("yandexaikit: raw: auth: %w", err)
	}

	start := c.clock.Now()
	resp, err := c.hc.Do(req)
	status := "error"
	if resp != nil {
		status = strconv.Itoa(resp.StatusCode)
	}
	c.recordCall(ctx, "native-passthrough", "", status, c.clock.Now().Sub(start))
	if err != nil {
		return nil, fmt.Errorf("yandexaikit: raw: %w", err)
	}
	return resp, nil
}
