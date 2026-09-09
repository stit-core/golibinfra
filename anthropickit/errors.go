package anthropickit

import (
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// Sentinel-ошибки кита. Потребитель ветвится через errors.Is на них, а не на
// типах/строках SDK. Refusal — НЕ ошибка: это успешный 200 со StopReason=refusal
// (см. Response.IsRefusal).
var (
	// ErrConfig — Config не прошёл валидацию.
	ErrConfig = errors.New("anthropickit: invalid config")
	// ErrAuth — API отверг учётные данные (401/403).
	ErrAuth = errors.New("anthropickit: authentication failed")
	// ErrInvalidRequest — запрос отвергнут API (400/413/422) или маппингом кита
	// ещё до вызова.
	ErrInvalidRequest = errors.New("anthropickit: invalid request")
	// ErrNotFound — неизвестная модель или endpoint (404).
	ErrNotFound = errors.New("anthropickit: not found")
	// ErrRateLimited — исчерпан лимит запросов (429).
	ErrRateLimited = errors.New("anthropickit: rate limited")
	// ErrOverloaded — API перегружен (529).
	ErrOverloaded = errors.New("anthropickit: overloaded")
	// ErrServer — повторяемый сбой API (408, 409, 5xx кроме 529).
	ErrServer = errors.New("anthropickit: server error")
	// ErrStreamNotDrained — Final вызван до Next()==false: дрейньте стрим, а учёт
	// брошенного берите из Stream.Usage.
	ErrStreamNotDrained = errors.New("anthropickit: stream is not drained")
	// ErrMaxTokensRequiresStreaming — MaxTokens sync-вызова выше потолка
	// non-streaming-режима: нужен Stream.
	ErrMaxTokensRequiresStreaming = errors.New("anthropickit: max_tokens requires streaming")
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
	case 408, 409:
		return fmt.Errorf("%w: %w", ErrServer, err)
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
