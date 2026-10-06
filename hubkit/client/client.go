// Package client — типизированный клиент hub API поверх httpclient: три
// backend'а хаба, sentinel-ошибки и sync-фид проекций.
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
	"git.stit.tech/stit-core/golibinfra/internal/httpdoer"
)

var (
	// ErrConfig — невалидный конфиг клиента (Config.Validate).
	ErrConfig = errors.New("hubkit/client: invalid config")
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
type Doer = httpdoer.Doer

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
// идемпотентных, breaker). Конфиг валидируется: клиент без адреса хаба не
// собирается.
func New(cfg Config, doer Doer) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if doer == nil {
		var err error
		if doer, err = defaultDoer(); err != nil {
			return nil, err
		}
	}
	return &Client{
		companyBase:  strings.TrimRight(cfg.BaseURL, "/"),
		employeeBase: strings.TrimRight(cfg.employeeBaseURL(), "/"),
		tagBase:      strings.TrimRight(cfg.tagBaseURL(), "/"),
		token:        cfg.ServiceToken,
		service:      cfg.ServiceName,
		http:         doer,
	}, nil
}

func defaultDoer() (Doer, error) {
	c, err := httpclient.New(httpclient.Config{
		Timeout:          defaultTimeout,
		MaxRetries:       3,
		RetryWait:        200 * time.Millisecond,
		RetryMaxWait:     5 * time.Second,
		MaxRedirects:     10,
		SSEMaxEventBytes: 1 << 20,
		Breaker:          httpclient.BreakerConfig{FailureThreshold: 5, RecoveryTimeout: 30 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("hubkit/client: default http client: %w", err)
	}
	return c, nil
}

// APIError — необёрнутая ошибка со статусом (для кодов вне NotFound/Conflict/…).
type APIError struct {
	Status int
	Body   string
}

// Error — текст ошибки со статусом и усечённым телом ответа хаба.
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
		// Транспорт/breaker — хаб недоступен; цепочку сохраняем целиком, чтобы
		// вызывающий мог отличить дедлайн от разомкнутого breaker'а.
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() {
		// Недочитанное тело закрывает keep-alive соединение: дренируем остаток.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
	}()

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
