package anthropickit

import (
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// Sentinel-ошибки кита. Потребитель ветвится через errors.Is на них, а не на
// типах/строках SDK. Refusal — НЕ ошибка: это успешный 200 с StopReason=refusal
// (см. Response.IsRefusal), здесь только транспортные/протокольные сбои API.
var (
	ErrConfig         = errors.New("anthropickit: invalid config")
	ErrAuth           = errors.New("anthropickit: authentication failed")
	ErrInvalidRequest = errors.New("anthropickit: invalid request")
	ErrNotFound       = errors.New("anthropickit: not found")
	ErrRateLimited    = errors.New("anthropickit: rate limited")
	ErrOverloaded     = errors.New("anthropickit: overloaded")
	ErrServer         = errors.New("anthropickit: server error")
)

// classify мапит ошибку SDK на sentinel по HTTP-статусу, сохраняя оригинал
// через %w (errors.Is видит и sentinel, и исходную *anthropic.Error).
func classify(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *anthropic.Error
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
	case 529:
		return fmt.Errorf("%w: %w", ErrOverloaded, err)
	default:
		if apiErr.StatusCode >= 500 {
			return fmt.Errorf("%w: %w", ErrServer, err)
		}
		return err
	}
}
