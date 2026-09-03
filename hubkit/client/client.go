// Package client — типизированный клиент hub API поверх httpclient (retry
// идемпотентных запросов, circuit breaker, OTel, X-Request-ID).
//
// Ресурсы: companies/projects/visibility + entity-tags (company-backend,
// api.hub.stit.tech), employees/departments (employee-backend, id.stit.tech)
// и tags (tag-backend, tags.hub.stit.tech). m2m-аутентификация статик-Bearer'ом
// (HUB_SERVICE_TOKEN). Ошибки типизированы (ErrNotFound / ErrConflict /
// ErrUnavailable) — продукту нужно отличать «хаб недоступен» (деградация
// чтения) от бизнес-ошибки.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.stit.tech/stit-core/golibinfra/httpclient"
)

var (
	// ErrNotFound — 404: ресурс отсутствует.
	ErrNotFound = errors.New("hubkit/client: not found")
	// ErrConflict — 409: конфликт (дубль slug/inn, оптимистичная блокировка).
	ErrConflict = errors.New("hubkit/client: conflict")
	// ErrUnavailable — хаб недоступен (транспорт, 5xx, breaker open): продукт
	// деградирует на локальную проекцию, а не показывает бизнес-ошибку.
	ErrUnavailable = errors.New("hubkit/client: hub unavailable")
	// ErrUnauthorized — 401/403: неверный/просроченный SERVICE_TOKEN.
	ErrUnauthorized = errors.New("hubkit/client: unauthorized")
)

const (
	defaultTimeout = 10 * time.Second
	// defaultRequestTimeout — потолок на весь запрос (все попытки retry) для
	// ctx без дедлайна: sync-страница на 500 записей укладывается с запасом.
	defaultRequestTimeout = 30 * time.Second
	maxErrorBody          = 4096
)

// Doer — HTTP-транспорт. Ему удовлетворяет *httpclient.Client (прод) и
// *http.Client (тесты).
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client — типизированный доступ к hub API.
type Client struct {
	companyBase  string
	employeeBase string
	tagBase      string
	token        string
	service      string
	http         Doer
}

// New строит Client из Config поверх переданного Doer (обычно
// *httpclient.Client); nil → httpclient с дефолтами (таймаут 10s, retry
// идемпотентных, breaker).
func New(cfg Config, doer Doer) *Client {
	if doer == nil {
		doer = defaultDoer()
	}
	return &Client{
		companyBase:  strings.TrimRight(cfg.BaseURL, "/"),
		employeeBase: strings.TrimRight(cfg.employeeBaseURL(), "/"),
		tagBase:      strings.TrimRight(cfg.tagBaseURL(), "/"),
		token:        cfg.ServiceToken,
		service:      cfg.ServiceName,
		http:         doer,
	}
}

func defaultDoer() Doer {
	c, err := httpclient.New(httpclient.Config{
		Timeout:      defaultTimeout,
		MaxRetries:   3,
		RetryWait:    200 * time.Millisecond,
		RetryMaxWait: 5 * time.Second,
		Breaker:      httpclient.BreakerConfig{FailureThreshold: 5, RecoveryTimeout: 30 * time.Second},
	})
	if err != nil {
		return &http.Client{Timeout: defaultTimeout}
	}
	return c
}

// APIError — необёрнутая ошибка со статусом (для кодов вне NotFound/Conflict/…).
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("hubkit/client: unexpected status %d: %s", e.Status, e.Body)
}

func (c *Client) do(ctx context.Context, method, base, path string, body, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("hubkit/client: marshal body: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return fmt.Errorf("hubkit/client: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.service != "" {
		req.Header.Set("X-Hub-Service", c.service)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Транспорт/breaker — хаб недоступен.
		return fmt.Errorf("%w: %s", ErrUnavailable, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()

	if err := statusErr(resp); err != nil {
		return err
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("hubkit/client: decode response: %w", err)
	}
	return nil
}

func statusErr(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusConflict:
		return ErrConflict
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return &APIError{Status: resp.StatusCode, Body: string(b)}
	}
}

// listQuery строит ?limit=&offset=&include_archived= из ListParams.
func listQuery(p ListParams) string {
	q := make([]string, 0, 3)
	if p.Limit > 0 {
		q = append(q, "limit="+strconv.Itoa(p.Limit))
	}
	if p.Offset > 0 {
		q = append(q, "offset="+strconv.Itoa(p.Offset))
	}
	if p.IncludeArchived {
		q = append(q, "include_archived=true")
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + strings.Join(q, "&")
}

// dataEnvelope — huma-ответы сервисов заворачивают полезную нагрузку в {data:…}.
type dataEnvelope[T any] struct {
	Data T `json:"data"`
}
