package amocrmkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// maxResponseBody — потолок тела успешного ответа: страница REST v4 (≤250
// сущностей) и объекты Drive укладываются в единицы MiB.
const maxResponseBody = 32 << 20

// maxIdleConnsPerHost — как в httpclient: у gateway каждый аккаунт — свой хост.
const maxIdleConnsPerHost = 100

// Client — транспортный клиент к amoCRM (REST v4 + amoJo + OAuth2 + Drive). Один
// экземпляр обслуживает все аккаунты: координаты аккаунта приходят аргументами.
type Client struct {
	cfg   Config
	hcCfg httpclient.Config
	hc    *httpclient.Client
	clock clockkit.Clock
	log   logkit.Logger
	rl    *accountLimiter
	instr *instruments

	hostOverride *url.URL // nil в проде; см. Config.HostOverride
}

// Option — параметр сборки Client, не выражаемый через Config.
type Option func(*options)

type options struct {
	hc        *httpclient.Client
	transport http.RoundTripper
}

// WithHTTPClient подставляет готовый httpclient вместо собранного китом (тесты,
// общий клиент): его ретраи не проходят через лимитер аккаунта и не поднимают
// retry_after из тела 429 — для боевой проводки передавайте httpclient.Config.
func WithHTTPClient(hc *httpclient.Client) Option {
	return func(o *options) { o.hc = hc }
}

// WithTransport — базовый транспорт для httpclient, который собирает кит
// (тесты, proxy). Игнорируется вместе с WithHTTPClient.
func WithTransport(rt http.RoundTripper) Option {
	return func(o *options) { o.transport = rt }
}

// New собирает Client: валидирует обе конфигурации и поднимает свой httpclient
// (retry/breaker/OTel) с лимитером аккаунта на каждой попытке.
func New(cfg Config, hcCfg httpclient.Config, clock clockkit.Clock, log logkit.Logger, opts ...Option) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Нулевой таймаут Validate отвергает: клиента без таймаута кит не собирает.
	if err := hcCfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: httpclient: %w", ErrConfig, err)
	}
	// Мутации amoCRM не идемпотентны и не дедуплицируются сервером.
	hcCfg.RetryNonIdempotent = false
	if clock == nil {
		clock = clockkit.Real{}
	}
	if log == nil {
		log = logkit.Nop()
	}
	o := options{}
	for _, fn := range opts {
		fn(&o)
	}

	rl := newAccountLimiter(cfg.RateRPS, cfg.MaxWait, limiterIdleTTL, clock)
	hc := o.hc
	if hc == nil {
		base := o.transport
		if base == nil {
			base = defaultTransport()
		}
		hcOpts := []httpclient.Option{
			httpclient.WithBeforeAttempt(rl.beforeAttempt),
			httpclient.WithTransport(&rateLimitTransport{next: base, rl: rl}),
		}
		var err error
		if hc, err = httpclient.New(hcCfg, hcOpts...); err != nil {
			return nil, fmt.Errorf("%w: httpclient: %w", ErrConfig, err)
		}
	}

	var override *url.URL
	if cfg.HostOverride != "" {
		override, _ = parseHostOverride(cfg.HostOverride) // проверено в Validate
	}
	return &Client{
		cfg:          cfg,
		hcCfg:        hcCfg,
		hc:           hc,
		clock:        clock,
		log:          log,
		rl:           rl,
		instr:        newInstruments(),
		hostOverride: override,
	}, nil
}

func defaultTransport() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	c := t.Clone()
	c.MaxIdleConnsPerHost = maxIdleConnsPerHost
	return c
}

// Account — координаты вызова к REST v4 / Drive одного аккаунта. Не состояние:
// потребитель заполняет его из своего token-store на каждый вызов.
type Account struct {
	Subdomain string // напр. "mycompany"
	TLD       string // "ru" | "com" (.ru и .com невзаимозаменяемы)
	Token     string // OAuth access token (Bearer)
}

func (a Account) restBase() string {
	return "https://" + a.Subdomain + ".amocrm." + a.TLD
}

func (a Account) ref() accountRef {
	return accountRef{Subdomain: a.Subdomain, TLD: a.TLD}
}

func (a Account) validate() error {
	return validateAccount(a.Subdomain, a.TLD)
}

// validateAccount отсекает всё, что не похоже на координаты amoCRM, до сборки
// URL: subdomain/tld попадают в host, а с ним уходят Bearer и client_secret.
func validateAccount(subdomain, tld string) error {
	if !validSubdomain(subdomain) {
		return fmt.Errorf("%w: subdomain %q", ErrInvalidAccount, subdomain)
	}
	if tld != "ru" && tld != "com" {
		return fmt.Errorf("%w: tld %q", ErrInvalidAccount, tld)
	}
	return nil
}

// validSubdomain — ^[a-z0-9-]{1,63}$.
func validSubdomain(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// seg экранирует динамический сегмент пути: id, uid и uuid приходят из внешних
// систем, а «/», «?» и «#» в них переписали бы адрес вызова. Пустое значение и
// dot-сегменты экранирование не ловит — их отвергает checkPath в resolveURL.
func seg(s string) string { return url.PathEscape(s) }

// checkPath отвергает пустые и dot-сегменты собранного пути: url.PathEscape не
// трогает точки, а сервер, нормализующий путь, выполнил бы по «..» не тот вызов,
// о котором просил потребитель.
func checkPath(escaped string) error {
	if escaped == "" || escaped == "/" {
		return nil
	}
	for _, s := range strings.Split(strings.TrimPrefix(escaped, "/"), "/") {
		if s == "" || s == "." || s == ".." {
			return fmt.Errorf("%w: path %q: empty or dot segment", ErrInvalidRequest, escaped)
		}
	}
	return nil
}

// entitySet — сущности, имя которых кит подставляет в путь как есть.
var entitySet = map[string]bool{"leads": true, "contacts": true, "companies": true, "customers": true}

// entitySegment проверяет имя сущности по allowlist: значение уходит в путь без
// экранирования, поэтому «/» в нём недопустим.
func entitySegment(entity string) (string, error) {
	if entitySet[entity] {
		return entity, nil
	}
	return "", fmt.Errorf("%w: unknown entity %q", ErrInvalidRequest, entity)
}

// customFieldsEntity — allowlist сущностей с доп-полями: к обычным добавляется
// catalogs/{id} (доп-поля элементов каталога).
func customFieldsEntity(entity string) (string, error) {
	if entitySet[entity] {
		return entity, nil
	}
	if id, ok := strings.CutPrefix(entity, "catalogs/"); ok && isDigits(id) {
		return entity, nil
	}
	return "", fmt.Errorf("%w: unknown custom fields entity %q", ErrInvalidRequest, entity)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// resolveURL собирает адрес вызова; при HostOverride подменяет только
// scheme://host, сохраняя путь и query (upload_url Drive несёт путь).
func (c *Client) resolveURL(base, path string, q url.Values) (*url.URL, error) {
	full := strings.TrimSuffix(base, "/") + path
	u, err := url.Parse(full)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return nil, fmt.Errorf("%w: url %q", ErrInvalidRequest, full)
	}
	if err := checkPath(u.EscapedPath()); err != nil {
		return nil, err
	}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	if c.hostOverride != nil {
		u.Scheme, u.Host = c.hostOverride.Scheme, c.hostOverride.Host
	}
	return u, nil
}

// checkExternalHost допускает адреса Drive только на хостах amoCRM аккаунта:
// drive_url и upload_url приходят от сервера и из token-store потребителя, а с
// запросом уходит Bearer. При HostOverride проверка выключена (sandbox/e2e).
func (c *Client) checkExternalHost(u *url.URL, acc Account) error {
	if c.hostOverride != nil {
		return nil
	}
	host := u.Hostname()
	suffix := ".amocrm." + acc.TLD
	if u.Scheme != "https" || !strings.HasSuffix(host, suffix) {
		return fmt.Errorf("%w: host %q is not amoCRM", ErrInvalidAccount, u.Host)
	}
	return nil
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
	// single — ответ описывает одну сущность: пустое тело значит «не найдена».
	single bool
	// allowRetry — повтор запроса безопасен вопреки методу (ephemeral-вызовы amoJo).
	allowRetry bool
}

// doREST выполняет аутентифицированный REST v4-запрос и декодит JSON в out.
func (c *Client) doREST(ctx context.Context, r restReq, out any) error {
	if err := r.acc.validate(); err != nil {
		return fmt.Errorf("amocrmkit: %s: %w", r.endpoint, err)
	}
	base := r.acc.restBase()
	if r.base != "" {
		base = r.base
	}
	u, err := c.resolveURL(base, r.path, r.query)
	if err != nil {
		return fmt.Errorf("amocrmkit: %s: %w", r.endpoint, err)
	}
	if r.base != "" {
		if err := c.checkExternalHost(u, r.acc); err != nil {
			return fmt.Errorf("amocrmkit: %s: %w", r.endpoint, err)
		}
	}

	var body io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return fmt.Errorf("amocrmkit: %s: %w: marshal request: %w", r.endpoint, ErrInvalidRequest, err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, r.method, u.String(), body)
	if err != nil {
		return fmt.Errorf("amocrmkit: %s: %w: new request: %w", r.endpoint, ErrInvalidRequest, err)
	}
	req.Header.Set("Authorization", "Bearer "+r.acc.Token)
	req.Header.Set("Accept", "application/json")
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.execute(ctx, req, call{
		endpoint:   r.endpoint,
		acc:        r.acc.ref(),
		single:     r.single,
		allowRetry: r.allowRetry,
	}, out)
}

// call — контекст одного вызова для execute: общий для REST/amoJo/Drive/OAuth.
type call struct {
	endpoint   string
	acc        accountRef
	single     bool
	allowRetry bool
}

// execute — общий путь всех контуров: throttle аккаунта → hc.Do (ретраи тоже
// через лимитер) → метрики → классификация статуса → декод тела. req должен
// быть полностью собран (URL, заголовки, тело) вызывающим.
func (c *Client) execute(ctx context.Context, req *http.Request, cl call, out any) error {
	ctx = withAccount(ctx, cl.acc)
	if cl.allowRetry {
		ctx = httpclient.AllowRetry(ctx)
	} else if req.Method == http.MethodPost || req.Method == http.MethodPatch {
		// Запрос без GetBody httpclient не повторит — request-level запрет повтора.
		req.GetBody = nil
	}
	req = req.WithContext(ctx)

	if err := c.rl.wait(ctx, cl.acc.key()); err != nil {
		c.countRateLimited(ctx, cl, err)
		return fmt.Errorf("amocrmkit: %s: %w", cl.endpoint, err)
	}

	start := c.clock.Now()
	resp, err := c.hc.Do(req)
	dur := c.clock.Now().Sub(start).Seconds()

	statusLabel := "error"
	if resp != nil {
		statusLabel = strconv.Itoa(resp.StatusCode)
	}
	c.instr.apiCalls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("endpoint", cl.endpoint),
		attribute.String("status", statusLabel),
	))
	c.instr.apiDuration.Record(ctx, dur, metric.WithAttributes(
		attribute.String("endpoint", cl.endpoint),
	))
	if err != nil {
		c.countRateLimited(ctx, cl, err)
		c.log.Warn(ctx, "amocrmkit: request failed", "endpoint", cl.endpoint, "account", cl.acc.Subdomain, "error", err.Error())
		return fmt.Errorf("amocrmkit: %s: %w", cl.endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		apiErr := c.parseAPIError(resp)
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			c.rl.penalize(cl.acc.key(), time.Duration(apiErr.RetryAfter)*time.Second)
			c.instr.rateLimited.Add(ctx, 1, metric.WithAttributes(
				attribute.String("tld", cl.acc.TLD),
				attribute.String("result", "remote"),
			))
			c.log.Warn(ctx, "amocrmkit: rate limited", "endpoint", cl.endpoint, "account", cl.acc.Subdomain, "retry_after", apiErr.RetryAfter)
		case resp.StatusCode >= 500:
			c.log.Warn(ctx, "amocrmkit: server error", "endpoint", cl.endpoint, "account", cl.acc.Subdomain, "status", resp.StatusCode, "trace_id", apiErr.TraceID)
		}
		return classify(resp.StatusCode, apiErr)
	}

	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
		return nil
	}
	if resp.StatusCode == http.StatusNoContent {
		return c.emptyResponse(cl)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return fmt.Errorf("amocrmkit: %s: read body: %w", cl.endpoint, err)
	}
	if len(raw) > maxResponseBody {
		return fmt.Errorf("amocrmkit: %s: %w: over %d bytes", cl.endpoint, ErrResponseTooLarge, maxResponseBody)
	}
	if len(raw) == 0 {
		return c.emptyResponse(cl)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("amocrmkit: %s: decode: %w", cl.endpoint, err)
	}
	return nil
}

// emptyResponse: для списка и батча пустой ответ — «нет данных», для одиночной
// сущности он неотличим от 404, поэтому ErrNotFound.
func (c *Client) emptyResponse(cl call) error {
	if cl.single {
		return fmt.Errorf("amocrmkit: %s: %w", cl.endpoint, ErrNotFound)
	}
	return nil
}

// countRateLimited считает вызовы, отклонённые лимитером до сети (пауза после
// 429 или переполненная очередь слотов).
func (c *Client) countRateLimited(ctx context.Context, cl call, err error) {
	if !errors.Is(err, ErrRateLimited) {
		return
	}
	c.instr.rateLimited.Add(ctx, 1, metric.WithAttributes(
		attribute.String("tld", cl.acc.TLD),
		attribute.String("result", "local"),
	))
}

// parseAPIError читает тело ошибочного ответа (application/problem+json). Никогда
// не возвращает nil: даже при нечитаемом/пустом теле отдаёт *APIError со статусом
// (sentinel выбирается отдельно по коду в classify).
func (c *Client) parseAPIError(resp *http.Response) *APIError {
	e := &APIError{Status: resp.StatusCode}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err == nil && len(raw) > 0 {
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
	}
	if e.RetryAfter == 0 {
		e.RetryAfter = retryAfterSeconds(resp.Header, c.clock.Now())
	}
	e.RetryAfter = min(e.RetryAfter, maxRetryAfterSeconds)
	return e
}

// maxRetryAfterSeconds — потолок паузы из ответа: дольше суток amoCRM не просит,
// а Duration от абсурдного значения переполняется в отрицательную.
const maxRetryAfterSeconds = 24 * 60 * 60

// retryAfterSeconds — пауза из заголовка Retry-After в секундах (вверх до целой),
// 0 при отсутствующем, нечитаемом или уже прошедшем значении. Разбор обеих форм
// RFC 9110 общий с httpclient.
func retryAfterSeconds(h http.Header, now time.Time) int {
	d, ok := httpclient.ParseRetryAfter(h.Get("Retry-After"), now)
	if !ok || d <= 0 {
		return 0
	}
	return min(int(math.Ceil(d.Seconds())), maxRetryAfterSeconds)
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

// Page — типизированная страница list-ответа REST v4.
type Page[T any] struct {
	Page       int
	TotalItems int
	Links      Links
	Items      []T
}

// listEntities GET'ит список и достаёт _embedded[key] как []T.
func listEntities[T any](ctx context.Context, c *Client, acc Account, endpoint, path, key string, q url.Values) (Page[T], error) {
	var env listEnvelope
	if err := c.doREST(ctx, restReq{acc: acc, method: http.MethodGet, path: path, query: q, endpoint: endpoint}, &env); err != nil {
		return Page[T]{}, err
	}
	page, err := embeddedPage[T](env, key)
	if err != nil {
		return page, fmt.Errorf("amocrmkit: %s: %w", endpoint, err)
	}
	return page, nil
}

// embeddedPage строит Page[T] из конверта, доставая _embedded[key].
func embeddedPage[T any](env listEnvelope, key string) (Page[T], error) {
	page := Page[T]{Page: env.Page, TotalItems: env.TotalItems, Links: env.Links}
	if raw, ok := env.Embedded[key]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &page.Items); err != nil {
			return page, fmt.Errorf("decode _embedded.%s: %w", key, err)
		}
	}
	return page, nil
}

// getOne GET'ит одиночный объект в T (для /{id} и /account); пустой ответ —
// ErrNotFound.
func getOne[T any](ctx context.Context, c *Client, acc Account, endpoint, path string, q url.Values) (T, error) {
	var out T
	err := c.doREST(ctx, restReq{acc: acc, method: http.MethodGet, path: path, query: q, endpoint: endpoint, single: true}, &out)
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
		v.Set("with", strings.Join(p.With, ","))
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

// withValues — query одиночного GET: with=a,b.
func withValues(with []string) url.Values {
	if len(with) == 0 {
		return nil
	}
	return url.Values{"with": {strings.Join(with, ",")}}
}
