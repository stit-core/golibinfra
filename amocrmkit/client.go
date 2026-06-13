package amocrmkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Client — транспортный клиент к amoCRM (REST v4 + amoJo + OAuth2). Один
// экземпляр обслуживает все аккаунты: координаты аккаунта приходят аргументами.
type Client struct {
	cfg   Config
	hc    *httpclient.Client
	clock clockkit.Clock
	log   logkit.Logger
	rl    *accountLimiter
	instr *instruments

	// hostOverride — тест-сид: если задан, все запросы идут на этот scheme://host
	// (httptest), путь/заголовки/подпись сохраняются. В продакшене пуст (host
	// берётся из subdomain/tld). httpclient не принимает кастомный transport,
	// поэтому редирект хоста — единственный способ дотянуться до httptest.
	hostOverride string
}

// New собирает Client поверх httpclient (retry/breaker/OTel), clockkit и logkit.
func New(cfg Config, hc *httpclient.Client, clock clockkit.Clock, log logkit.Logger) (*Client, error) {
	if hc == nil {
		return nil, fmt.Errorf("amocrmkit: httpclient is required")
	}
	if cfg.RateRPS <= 0 {
		return nil, fmt.Errorf("amocrmkit: RateRPS must be > 0, got %d", cfg.RateRPS)
	}
	if clock == nil {
		clock = clockkit.Real{}
	}
	if log == nil {
		log = logkit.Nop()
	}
	return &Client{
		cfg:   cfg,
		hc:    hc,
		clock: clock,
		log:   log,
		rl:    newAccountLimiter(cfg.RateRPS),
		instr: newInstruments(),
	}, nil
}

// Account — координаты вызова к REST v4 / Drive одного аккаунта. Не состояние:
// заполняется gateway из token-store на каждый вызов.
type Account struct {
	Subdomain string // напр. "mycompany"
	TLD       string // "ru" | "com" (.ru и .com невзаимозаменяемы)
	Token     string // OAuth access token (Bearer)
}

func (a Account) restBase() string {
	return "https://" + a.Subdomain + ".amocrm." + a.TLD
}

// resolveURL собирает финальный URL, учитывая тест-сид hostOverride.
func (c *Client) resolveURL(base, path string, q url.Values) string {
	if c.hostOverride != "" {
		base = c.hostOverride
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// restReq — параметры одного REST v4-вызова.
type restReq struct {
	acc      Account
	base     string // переопределяет restBase (Drive ходит на динамический drive_url)
	method   string
	path     string // напр. "/api/v4/leads"
	query    url.Values
	body     any    // маршалится в JSON, если не nil
	endpoint string // метка метрики, напр. "leads.list"
}

// doREST выполняет аутентифицированный REST v4-запрос и декодит JSON в out
// (если out != nil). 204 / пустое тело → out не трогается, ошибки нет.
func (c *Client) doREST(ctx context.Context, r restReq, out any) error {
	base := r.acc.restBase()
	if r.base != "" {
		base = r.base
	}
	u := c.resolveURL(base, r.path, r.query)

	var body io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return fmt.Errorf("amocrmkit: %s: marshal request: %w", r.endpoint, err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, r.method, u, body)
	if err != nil {
		return fmt.Errorf("amocrmkit: %s: new request: %w", r.endpoint, err)
	}
	req.Header.Set("Authorization", "Bearer "+r.acc.Token)
	req.Header.Set("Accept", "application/json")
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.execute(ctx, req, r.endpoint, r.acc.Subdomain, out)
}

// execute — общий путь для REST/amoJo/Drive/OAuth: per-account throttle → hc.Do →
// метрики → классификация статуса → декод тела. req должен быть полностью собран
// (URL, заголовки, тело) вызывающим.
func (c *Client) execute(ctx context.Context, req *http.Request, endpoint, account string, out any) error {
	if err := c.rl.wait(ctx, account); err != nil {
		return fmt.Errorf("amocrmkit: %s: rate limiter: %w", endpoint, err)
	}

	start := c.clock.Now()
	resp, err := c.hc.Do(req)
	dur := c.clock.Now().Sub(start).Seconds()

	statusLabel := "error"
	if resp != nil {
		statusLabel = strconv.Itoa(resp.StatusCode)
	}
	c.instr.apiCalls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("endpoint", endpoint),
		attribute.String("status", statusLabel),
	))
	c.instr.apiDuration.Record(ctx, dur, metric.WithAttributes(
		attribute.String("endpoint", endpoint),
	))
	if err != nil {
		return fmt.Errorf("amocrmkit: %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			c.instr.rateLimited.Add(ctx, 1, metric.WithAttributes(
				attribute.String("account", account),
			))
		}
		return classify(resp.StatusCode, parseAPIError(resp))
	}

	if resp.StatusCode == http.StatusNoContent || out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("amocrmkit: %s: read body: %w", endpoint, err)
	}
	if len(raw) == 0 {
		return nil // 200 без тела (часть update/pin) — не ошибка
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("amocrmkit: %s: decode: %w", endpoint, err)
	}
	return nil
}

// parseAPIError читает тело ошибочного ответа (application/problem+json). Никогда
// не возвращает nil: даже при нечитаемом/пустом теле отдаёт *APIError со статусом
// (sentinel выбирается отдельно по коду в classify).
func parseAPIError(resp *http.Response) *APIError {
	e := &APIError{Status: resp.StatusCode}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(raw) == 0 {
		// retry_after может прийти и заголовком — подстрахуемся.
		e.RetryAfter = headerSeconds(resp, "Retry-After")
		return e
	}

	var b apiErrorBody
	if json.Unmarshal(raw, &b) == nil {
		e.Title, e.Type, e.Detail = b.Title, b.Type, b.Detail
		e.TraceID = b.TraceID
		e.RetryAfter = b.RetryAfter
		for _, ve := range b.ValidationErrors {
			fe := ValidationError{RequestID: ve.RequestID}
			for _, x := range ve.Errors {
				fe.Errors = append(fe.Errors, FieldError{Code: x.Code, Path: x.Path, Detail: x.Detail})
			}
			e.ValidationErrors = append(e.ValidationErrors, fe)
		}
	}
	if e.RetryAfter == 0 {
		e.RetryAfter = headerSeconds(resp, "Retry-After")
	}
	return e
}

func headerSeconds(resp *http.Response, key string) int {
	if v := resp.Header.Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// apiErrorBody — wire-форма тела ошибки amoCRM (kebab-case validation-errors,
// retry_after как поле тела на 429, trace_id на 500).
type apiErrorBody struct {
	Title            string `json:"title"`
	Type             string `json:"type"`
	Status           int    `json:"status"`
	Detail           string `json:"detail"`
	RetryAfter       int    `json:"retry_after"`
	TraceID          string `json:"trace_id"`
	ValidationErrors []struct {
		RequestID string `json:"request_id"`
		Errors    []struct {
			Code   string `json:"code"`
			Path   string `json:"path"`
			Detail string `json:"detail"`
		} `json:"errors"`
	} `json:"validation-errors"`
}

// --- Generic-обёртки поверх doREST: сводят каждый метод-зеркало к одной строке. ---

// Page[T] — типизированная страница list-ответа REST v4.
type Page[T any] struct {
	Page       int
	TotalItems int
	Links      Links
	Items      []T
}

// listEntities GET'ит список и достаёт _embedded[key] как []T.
func listEntities[T any](ctx context.Context, c *Client, acc Account, endpoint, path, key string, q url.Values) (Page[T], error) {
	var env listEnvelope
	err := c.doREST(ctx, restReq{acc: acc, method: http.MethodGet, path: path, query: q, endpoint: endpoint}, &env)
	page := Page[T]{Page: env.Page, TotalItems: env.TotalItems, Links: env.Links}
	if err != nil {
		return page, err
	}
	if raw, ok := env.Embedded[key]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &page.Items); err != nil {
			return page, fmt.Errorf("amocrmkit: %s: decode _embedded.%s: %w", endpoint, key, err)
		}
	}
	return page, nil
}

// embeddedPage строит Page[T] из конверта, доставая _embedded[key].
func embeddedPage[T any](env listEnvelope, key string) (Page[T], error) {
	page := Page[T]{Page: env.Page, TotalItems: env.TotalItems, Links: env.Links}
	if raw, ok := env.Embedded[key]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &page.Items); err != nil {
			return page, fmt.Errorf("amocrmkit: decode _embedded.%s: %w", key, err)
		}
	}
	return page, nil
}

// getOne GET'ит одиночный объект в T (для /{id} и /account). 204/пусто → нулевой T.
func getOne[T any](ctx context.Context, c *Client, acc Account, endpoint, path string, q url.Values) (T, error) {
	var out T
	err := c.doREST(ctx, restReq{acc: acc, method: http.MethodGet, path: path, query: q, endpoint: endpoint}, &out)
	return out, err
}

// sendBatch шлёт массив items (POST/PATCH batch) и достаёт _embedded[key] как []Out.
func sendBatch[In, Out any](ctx context.Context, c *Client, acc Account, method, endpoint, path, key string, items []In) ([]Out, error) {
	var env listEnvelope
	if err := c.doREST(ctx, restReq{acc: acc, method: method, path: path, body: items, endpoint: endpoint}, &env); err != nil {
		return nil, err
	}
	var out []Out
	if raw, ok := env.Embedded[key]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("amocrmkit: %s: decode _embedded.%s: %w", endpoint, key, err)
		}
	}
	return out, nil
}

// sendOne шлёт одиночный объект (POST/PATCH /{id}) и декодит ответ в Out.
func sendOne[Out any](ctx context.Context, c *Client, acc Account, method, endpoint, path string, body any) (Out, error) {
	var out Out
	err := c.doREST(ctx, restReq{acc: acc, method: method, path: path, body: body, endpoint: endpoint}, &out)
	return out, err
}

// sendNoContent — вызовы без полезного тела ответа (delete/detach/accept/202/204).
func (c *Client) sendNoContent(ctx context.Context, acc Account, method, endpoint, path string, body any) error {
	return c.doREST(ctx, restReq{acc: acc, method: method, path: path, body: body, endpoint: endpoint}, nil)
}

// listEnvelope — общий конверт list/batch-ответов REST v4; _embedded декодится
// по ключу сущности (leads/contacts/…), значения — отложенно.
type listEnvelope struct {
	Page       int                        `json:"_page"`
	TotalItems int                        `json:"_total_items"`
	Links      Links                      `json:"_links"`
	Embedded   map[string]json.RawMessage `json:"_embedded"`
}

// --- Shared-типы пагинации REST v4 (_embedded / _links / _page / _total_items). ---

// Link — элемент HAL-навигации _links.
type Link struct {
	Href string `json:"href"`
}

// Links — стандартный блок _links list-ответа.
type Links struct {
	Self  *Link `json:"self,omitempty"`
	Next  *Link `json:"next,omitempty"`
	Prev  *Link `json:"prev,omitempty"`
	First *Link `json:"first,omitempty"`
	Last  *Link `json:"last,omitempty"`
}

// ListParams — общие query-параметры list-методов REST v4.
type ListParams struct {
	Page   int               // _page
	Limit  int               // limit (≤250)
	With   []string          // with=a,b
	Query  string            // query (полнотекст)
	Filter map[string]string // filter[k]=v
	Order  map[string]string // order[k]=asc|desc
}

func (p ListParams) values() url.Values {
	v := url.Values{}
	if p.Page > 0 {
		v.Set("page", strconv.Itoa(p.Page))
	}
	if p.Limit > 0 {
		v.Set("limit", strconv.Itoa(p.Limit))
	}
	if len(p.With) > 0 {
		v.Set("with", joinComma(p.With))
	}
	if p.Query != "" {
		v.Set("query", p.Query)
	}
	for k, val := range p.Filter {
		v.Set("filter["+k+"]", val)
	}
	for k, val := range p.Order {
		v.Set("order["+k+"]", val)
	}
	return v
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
