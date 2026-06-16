package openaikit

import (
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3"
)

// Sentinel-ошибки кита. Потребитель ветвится через errors.Is на них, а не на
// типах/строках SDK. Refusal — НЕ ошибка: это успешный 200 с refusal-полем в
// ответе (см. *Response.Refusal); здесь только транспортные/протокольные сбои API.
var (
	ErrConfig         = errors.New("openaikit: invalid config")
	ErrAuth           = errors.New("openaikit: authentication failed")
	ErrPermission     = errors.New("openaikit: permission denied")
	ErrInvalidRequest = errors.New("openaikit: invalid request")
	ErrNotFound       = errors.New("openaikit: not found")
	ErrConflict       = errors.New("openaikit: conflict")
	ErrRateLimited    = errors.New("openaikit: rate limited")
	ErrServer         = errors.New("openaikit: server error")
)

// classify мапит ошибку SDK на sentinel по HTTP-статусу, сохраняя оригинал через
// %w (errors.Is видит и sentinel, и исходную *openai.Error). 429 у OpenAI — это
// rate-limit ИЛИ исчерпание квоты (insufficient_quota); оба → ErrRateLimited,
// различие — по error.type/code в исходной ошибке.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.StatusCode {
	case 400, 422:
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	case 401:
		return fmt.Errorf("%w: %w", ErrAuth, err)
	case 403:
		return fmt.Errorf("%w: %w", ErrPermission, err)
	case 404:
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case 409:
		return fmt.Errorf("%w: %w", ErrConflict, err)
	case 429:
		return fmt.Errorf("%w: %w", ErrRateLimited, err)
	default:
		if apiErr.StatusCode >= 500 {
			return fmt.Errorf("%w: %w", ErrServer, err)
		}
		return err
	}
}
