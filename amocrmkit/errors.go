package amocrmkit

import (
	"errors"
	"fmt"
)

// Таксономия ошибок: потребитель различает исходы через errors.Is и решает
// retry/throttle/dead-letter. HTTP-коды наружу не протекают.
var (
	// ErrConfig — невалидный Config кита или переданный ему httpclient.Config.
	ErrConfig = errors.New("amocrmkit: invalid config")
	// ErrInvalidAccount — subdomain/tld вне формата amoCRM либо drive_url на чужом хосте.
	ErrInvalidAccount = errors.New("amocrmkit: invalid account")
	// ErrInvalidRequest — запрос отвергнут китом до сети (перманентно): сегмент
	// пути вне allowlist, связь вне матрицы, несериализуемое тело.
	ErrInvalidRequest = errors.New("amocrmkit: invalid request")
	// ErrAuth — 401: токен истёк или отозван.
	ErrAuth = errors.New("amocrmkit: auth failed")
	// ErrForbidden — 403: нет прав либо аккаунт заблокирован за превышение rps.
	ErrForbidden = errors.New("amocrmkit: forbidden")
	// ErrPaymentRequired — 402: подписка аккаунта закончилась или функционал не оплачен.
	ErrPaymentRequired = errors.New("amocrmkit: payment required")
	// ErrNotFound — 404 либо пустой ответ на запрос одиночной сущности.
	ErrNotFound = errors.New("amocrmkit: not found")
	// ErrValidation — 400/422: тело запроса отвергнуто amoCRM (см. APIError.ValidationErrors).
	ErrValidation = errors.New("amocrmkit: validation error")
	// ErrRateLimited — 429 от amoCRM либо локальный отказ лимитера аккаунта.
	ErrRateLimited = errors.New("amocrmkit: rate limited")
	// ErrServer — 5xx.
	ErrServer = errors.New("amocrmkit: server error")
	// ErrResponseTooLarge — тело успешного ответа больше maxResponseBody.
	ErrResponseTooLarge = errors.New("amocrmkit: response too large")
)

// APIError несёт структурированную деталь ответа amoCRM (application/problem+json)
// рядом с sentinel'ом. Доступен через errors.As(err, &apiErr).
type APIError struct {
	Status           int
	Title            string
	Type             string
	Detail           string
	ValidationErrors []ValidationError
	RetryAfter       int    // секунды: поле тела retry_after или заголовок Retry-After (429)
	TraceID          string // 500
}

// Error возвращает текст ошибки amoCRM со статусом и деталью.
func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("amocrmkit: api %d %s: %s", e.Status, e.Title, e.Detail)
	}
	return fmt.Sprintf("amocrmkit: api %d %s", e.Status, e.Title)
}

// ValidationError — элемент validation-errors[] ответа 400: ошибки одного
// элемента батча, адресованного по request_id.
type ValidationError struct {
	RequestID string
	Errors    []FieldError
}

// FieldError — ошибка конкретного поля внутри ValidationError.
type FieldError struct {
	Code   string
	Path   string
	Detail string
}

// classify маппит HTTP-статус в sentinel и оборачивает apiErr (если есть),
// чтобы errors.Is видел и sentinel, и *APIError.
func classify(status int, apiErr error) error {
	sentinel := sentinelFor(status)
	if apiErr != nil {
		return fmt.Errorf("%w: %w", sentinel, apiErr)
	}
	return sentinel
}

func sentinelFor(status int) error {
	switch status {
	case 401:
		return ErrAuth
	case 402:
		return ErrPaymentRequired
	case 403:
		return ErrForbidden
	case 404:
		return ErrNotFound
	case 400, 422:
		return ErrValidation
	case 429:
		return ErrRateLimited
	}
	if status >= 500 {
		return ErrServer
	}
	// Прочие 4xx (405/409/...) — про запрос, а не про состояние аккаунта.
	return ErrValidation
}
