package googleaikit

import (
	"errors"
	"fmt"

	"google.golang.org/genai"
)

// Sentinel-ошибки кита. Потребитель ветвится через errors.Is на них, а не на
// типах/строках SDK. Маппинг — по HTTP-статусу google.rpc.Status (research §10):
// 429 RESOURCE_EXHAUSTED / 503 UNAVAILABLE ретраибельны, 4xx (кроме 429) — терминальны.
var (
	ErrConfig         = errors.New("googleaikit: invalid config")
	ErrAuth           = errors.New("googleaikit: authentication failed")
	ErrInvalidRequest = errors.New("googleaikit: invalid request")
	ErrNotFound       = errors.New("googleaikit: not found")
	ErrRateLimited    = errors.New("googleaikit: rate limited")
	ErrOverloaded     = errors.New("googleaikit: overloaded")
	ErrServer         = errors.New("googleaikit: server error")
	ErrOperation      = errors.New("googleaikit: long-running operation failed")
)

// classify мапит ошибку SDK на sentinel по HTTP-статусу google.rpc.Status,
// сохраняя оригинал через %w (errors.Is видит и sentinel, и исходную genai.APIError).
func classify(err error) error {
	if err == nil {
		return nil
	}
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Code {
	case 400, 422:
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	case 401, 403:
		return fmt.Errorf("%w: %w", ErrAuth, err)
	case 404:
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case 429:
		return fmt.Errorf("%w: %w", ErrRateLimited, err)
	case 503:
		return fmt.Errorf("%w: %w", ErrOverloaded, err)
	default:
		if apiErr.Code >= 500 {
			return fmt.Errorf("%w: %w", ErrServer, err)
		}
		return err
	}
}
