package yandexaikit

import (
	"errors"
	"fmt"
	"net/http"
)

// Sentinel-ошибки кита. Потребитель ветвится через errors.Is, а не на статусах.
// Транспорт Yandex FM — REST с телом google.rpc.Status; gRPC-код мапится в
// HTTP-статус (см. research §10.2). Здесь классифицируем по HTTP-статусу,
// сохраняя оригинал через %w (errors.Is видит и sentinel, и *APIError).
var (
	ErrConfig         = errors.New("yandexaikit: invalid config")
	ErrAuth           = errors.New("yandexaikit: authentication failed")
	ErrInvalidRequest = errors.New("yandexaikit: invalid request")
	ErrNotFound       = errors.New("yandexaikit: not found")
	ErrRateLimited    = errors.New("yandexaikit: rate limited")
	ErrServer         = errors.New("yandexaikit: server error")
	ErrUnavailable    = errors.New("yandexaikit: service unavailable")
	ErrTimeout        = errors.New("yandexaikit: deadline exceeded")
	// ErrOperationFailed — async-операция завершилась с error (done:true + error).
	ErrOperationFailed = errors.New("yandexaikit: operation failed")
)

// APIError несёт тело ответа Yandex (google.rpc.Status) рядом с sentinel'ом.
// Доступен через errors.As(err, &apiErr). Code — gRPC-код (int32).
type APIError struct {
	Status  int    // HTTP-статус
	Code    int32  // gRPC status code (google.rpc.Status.code)
	Message string // google.rpc.Status.message
}

func (e *APIError) Error() string {
	return fmt.Sprintf("yandexaikit: api %d (grpc %d): %s", e.Status, e.Code, e.Message)
}

// classify маппит HTTP-статус в sentinel и оборачивает *APIError (если есть),
// чтобы errors.Is видел и sentinel, и тело.
func classify(status int, apiErr *APIError) error {
	sentinel := sentinelFor(status)
	if apiErr != nil {
		return fmt.Errorf("%w: %w", sentinel, apiErr)
	}
	return sentinel
}

func sentinelFor(status int) error {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusConflict:
		return ErrInvalidRequest
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusServiceUnavailable:
		return ErrUnavailable
	case http.StatusGatewayTimeout:
		return ErrTimeout
	}
	if status >= 500 {
		return ErrServer
	}
	return ErrInvalidRequest
}
