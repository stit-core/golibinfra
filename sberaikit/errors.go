package sberaikit

import (
	"errors"
	"fmt"
)

// Sentinel-ошибки кита. Потребитель ветвится через errors.Is на них, а не на
// строках/статусах GigaChat. Здесь — транспортные/протокольные сбои API и
// конфигурация; усечения генерации (finish_reason) — не ошибки, а поле ответа.
var (
	ErrConfig         = errors.New("sberaikit: invalid config")
	ErrAuth           = errors.New("sberaikit: authentication failed")
	ErrInvalidRequest = errors.New("sberaikit: invalid request")
	ErrNotFound       = errors.New("sberaikit: not found")
	ErrRateLimited    = errors.New("sberaikit: rate limited")
	ErrServer         = errors.New("sberaikit: server error")
)

// APIError — структурированная ошибка GigaChat. Тело декодим в обеих формах
// (OpenAI-style {error:{...}} и Sber {status,message}) — см. parseAPIError; raw
// сохраняем для диагностики. Реализует error; sentinel выбирает classify по коду.
type APIError struct {
	StatusCode int
	Message    string
	Raw        string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("sberaikit: api error %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("sberaikit: api error %d", e.StatusCode)
}

// errInvalid / errServer — короткие обёртки sentinel+context для нетранспортных
// сбоев (маршалинг/декод/scan), чтобы errors.Is видел категорию.
func errInvalid(what string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrInvalidRequest, what, err)
}

func errServer(what string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrServer, what, err)
}

// classify мапит HTTP-статус на sentinel, сохраняя *APIError через %w
// (errors.Is видит и sentinel, и исходную *APIError).
func classify(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.StatusCode {
	case 400, 413, 422:
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	case 401, 403:
		return fmt.Errorf("%w: %w", ErrAuth, err)
	case 404:
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case 429:
		return fmt.Errorf("%w: %w", ErrRateLimited, err)
	default:
		if apiErr.StatusCode >= 500 {
			return fmt.Errorf("%w: %w", ErrServer, err)
		}
		return err
	}
}
