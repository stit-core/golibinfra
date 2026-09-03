package amocrmkit

import (
	"errors"
	"fmt"
)

// Таксономия ошибок. Потребитель различает исходы через errors.Is для решения
// retry/throttle/dead-letter. HTTP-коды наружу не протекают — только sentinel'ы
// (+ опционально *APIError через errors.As).
var (
	ErrConfig         = errors.New("amocrmkit: invalid config")
	ErrInvalidAccount = errors.New("amocrmkit: invalid account")  // subdomain/tld вне допустимого формата
	ErrAuth           = errors.New("amocrmkit: auth failed")      // 401
	ErrForbidden      = errors.New("amocrmkit: forbidden")        // 403 (в т.ч. IP-бан)
	ErrNotFound       = errors.New("amocrmkit: not found")        // 404
	ErrValidation     = errors.New("amocrmkit: validation error") // 400/422 (+ validation-errors)
	ErrRateLimited    = errors.New("amocrmkit: rate limited")     // 429 (+ retry_after)
	ErrServer         = errors.New("amocrmkit: server error")     // 5xx
)

// APIError несёт структурированную деталь ответа amoCRM (application/problem+json)
// рядом с sentinel'ом. Доступен через errors.As(err, &apiErr).
type APIError struct {
	Status           int
	Title            string
	Type             string
	Detail           string
	ValidationErrors []ValidationError
	RetryAfter       int    // секунды, поле тела retry_after (429)
	TraceID          string // 500
}

func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("amocrmkit: api %d %s: %s", e.Status, e.Title, e.Detail)
	}
	return fmt.Sprintf("amocrmkit: api %d %s", e.Status, e.Title)
}

// ValidationError — элемент validation-errors[] (есть только у 400).
type ValidationError struct {
	RequestID string
	Errors    []FieldError
}

// FieldError — конкретная ошибка поля.
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
	// Прочие 4xx (402/405/409/...) — клиентские; трактуем как validation.
	return ErrValidation
}
