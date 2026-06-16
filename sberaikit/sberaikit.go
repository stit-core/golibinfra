// Package sberaikit — клиент Sber GigaChat поверх raw net/http (без SDK):
// синхронный и стриминговый chat (SSE + [DONE]), embeddings, image-as-file
// (text2image → file-id → /files/{id}/content), нормализованный учёт токенов
// (Usage) и стоимость (Cost) в микро-рублях. OAuth2 client-credentials с RqUID и
// проактивным рефрешем (clockkit), TLS со scoped RootCAs Минцифры-CA (никогда
// InsecureSkipVerify). Эмитит sber_api_calls_total{op,model,status},
// sber_api_latency_seconds{op,model}, sber_api_tokens_total{model,kind},
// sber_api_cost_micros_total{model}. Без БД: хранение/леджер/агрегации — на
// стороне потребителя.
package sberaikit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// API — поверхность клиента для DI и моков у потребителя. Stream возвращает
// итератор; ошибки стрима — через Stream.Err.
type API interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) Stream
	Embeddings(ctx context.Context, req EmbeddingsRequest) (*EmbeddingsResponse, error)
	// FileContent тянет байты файла (image-as-file: байты сгенерированной картинки).
	FileContent(ctx context.Context, fileID string) ([]byte, string, error)
	// Raw — аутентифицированный passthrough к нативному GigaChat (NativePassthrough):
	// kit ставит Bearer-токен/RqUID/CA, тело и путь — как есть.
	Raw(ctx context.Context, method, path string, body io.Reader, hdr http.Header) (*http.Response, error)
}

type Client struct {
	cfg    Config
	hc     *http.Client
	tokens *tokenManager
	clock  clockkit.Clock
	log    logkit.Logger
	prices map[string]Price
	instr  *instruments
}

var _ API = (*Client)(nil)

// New собирает клиент: scoped-TLS http.Client (Минцифры-CA), token-manager,
// прайс-таблицу и инструменты. logkit/clockkit — опциональны (Nop/Real по nil).
func New(cfg Config, log logkit.Logger, clock clockkit.Clock, opts ...Option) (*Client, error) {
	if cfg.AuthKey == "" {
		return nil, fmt.Errorf("%w: SBER_AUTH_KEY is required", ErrConfig)
	}
	if log == nil {
		log = logkit.Nop()
	}
	if clock == nil {
		clock = clockkit.Real{}
	}

	o := options{prices: defaultPrices, baseURL: cfg.BaseURL}
	for _, fn := range opts {
		fn(&o)
	}
	if o.prices == nil {
		o.prices = defaultPrices
	}

	hc := o.httpClient
	if hc == nil {
		built, err := newHTTPClient(cfg)
		if err != nil {
			return nil, err
		}
		hc = built
	}

	return &Client{
		cfg:    cfg,
		hc:     hc,
		tokens: newTokenManager(cfg, hc, clock),
		clock:  clock,
		log:    log,
		prices: o.prices,
		instr:  newInstruments(),
	}, nil
}

// newHTTPClient строит выделенный http.Client со scoped RootCAs (Минцифры-CA) и
// OTel-инструментированным транспортом. httpclient.Client не годится: он не даёт
// задать TLSClientConfig, а CA-scoping — критичен (нельзя глобально доверять
// гос-CA). Retry/401-refresh — в самом ките (doJSON).
func newHTTPClient(cfg Config) (*http.Client, error) {
	tlsCfg, err := newTLSConfig(cfg.CABundlePath)
	if err != nil {
		return nil, err
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = tlsCfg
	return &http.Client{
		Timeout:   cfg.Timeout,
		Transport: otelhttp.NewTransport(base),
	}, nil
}

func (c *Client) model(m string) string {
	if m == "" {
		return c.cfg.DefaultModel
	}
	return m
}

func (c *Client) costOf(model string, u Usage) RUBMicros {
	if p, ok := c.prices[model]; ok {
		return p.cost(u)
	}
	return 0
}

// doRequest выполняет аутентифицированный HTTP-запрос с одним 401-refresh-retry и
// backoff-ретраями на 429/5xx (research §10). Заголовки auth/RqUID ставит сам;
// extra накладывает поверх. Тело должно быть re-seekable для ретраев → принимаем
// []byte и пересобираем reader на каждой попытке.
func (c *Client) doRequest(ctx context.Context, method, url string, body []byte, contentType string, extra http.Header) (*http.Response, error) {
	var lastErr error
	refreshed := false

	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}

		tok, err := c.tokens.token(ctx)
		if err != nil {
			return nil, err
		}

		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return nil, fmt.Errorf("%w: new request: %w", ErrInvalidRequest, err)
		}
		for k, vs := range extra {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set(rqUIDHeader, uuid.NewString())
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%w: %w", ErrServer, err)
			continue
		}

		// 401 → один refresh+retry (токен мог протухнуть между mint и вызовом).
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			resp.Body.Close()
			c.tokens.invalidate()
			refreshed = true
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = classify(parseAPIError(resp.StatusCode, resp.Body))
			resp.Body.Close()
			continue
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: exhausted retries", ErrServer)
	}
	return nil, lastErr
}

// doJSON — POST JSON-тело, декод JSON-ответа в out. Возвращает op-метку для
// метрик через вызывающего. На 4xx/5xx — classified error.
func (c *Client) doJSON(ctx context.Context, op, url string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("%w: marshal %s: %w", ErrInvalidRequest, op, err)
	}
	resp, err := c.doRequest(ctx, http.MethodPost, url, b, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return classify(parseAPIError(resp.StatusCode, resp.Body))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: decode %s: %w", ErrServer, op, err)
	}
	return nil
}

// FileContent тянет байты файла (image-as-file). Accept: application/jpg для
// картинок text2image (research §3). Возвращает (bytes, content-type).
func (c *Client) FileContent(ctx context.Context, fileID string) ([]byte, string, error) {
	if fileID == "" {
		return nil, "", fmt.Errorf("%w: empty file id", ErrInvalidRequest)
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/files/" + fileID + "/content"
	hdr := http.Header{"Accept": []string{"application/jpg"}}

	start := c.clock.Now()
	resp, err := c.doRequest(ctx, http.MethodGet, url, nil, "", hdr)
	if err != nil {
		c.instr.record(ctx, "files", "", "error", c.clock.Now().Sub(start), Usage{}, 0)
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		c.instr.record(ctx, "files", "", strconv.Itoa(resp.StatusCode), c.clock.Now().Sub(start), Usage{}, 0)
		return nil, "", classify(parseAPIError(resp.StatusCode, resp.Body))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("%w: read file body: %w", ErrServer, err)
	}
	c.instr.record(ctx, "files", "", "ok", c.clock.Now().Sub(start), Usage{}, 0)
	return raw, resp.Header.Get("Content-Type"), nil
}

// Raw — аутентифицированный passthrough к нативному GigaChat (NativePassthrough).
// Kit ставит Bearer/RqUID/CA; путь — относительный к BaseURL либо абсолютный.
// Тело ответа НЕ закрывается — это делает вызывающий (он же стримит наружу).
func (c *Client) Raw(ctx context.Context, method, path string, body io.Reader, hdr http.Header) (*http.Response, error) {
	url := path
	if !strings.HasPrefix(path, "http") {
		url = strings.TrimRight(c.cfg.BaseURL, "/") + "/" + strings.TrimLeft(path, "/")
	}
	var b []byte
	if body != nil {
		raw, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("%w: read raw body: %w", ErrInvalidRequest, err)
		}
		b = raw
	}
	ct := ""
	if hdr != nil {
		ct = hdr.Get("Content-Type")
	}
	return c.doRequest(ctx, method, url, b, ct, hdr)
}

// backoff — экспоненциальный backoff под номер попытки (1s, 2s, 4s, 8s; research §10).
func backoff(attempt int) time.Duration {
	d := time.Second << (attempt - 1)
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}
