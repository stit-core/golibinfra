package anthropickit

import "github.com/anthropics/anthropic-sdk-go"

// Usage — нормализованный учёт токенов одного запроса. Cache-write разбит по TTL
// (5m/1h) для точной стоимости. Поля server-tool отражают платные серверные
// инструменты Anthropic (web_search платный, web_fetch — нет).
type Usage struct {
	InputTokens        int64
	OutputTokens       int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	CacheReadTokens    int64
	ThinkingTokens     int64
	WebSearchRequests  int64
	WebFetchRequests   int64
}

// TotalInput — полный вход с учётом cache-write и cache-read
// (input + cache_creation + cache_read), как в инварианте Messages API.
func (u Usage) TotalInput() int64 {
	return u.InputTokens + u.CacheWrite5mTokens + u.CacheWrite1hTokens + u.CacheReadTokens
}

func mapUsage(u anthropic.Usage) Usage {
	out := Usage{
		InputTokens:        u.InputTokens,
		OutputTokens:       u.OutputTokens,
		CacheWrite5mTokens: u.CacheCreation.Ephemeral5mInputTokens,
		CacheWrite1hTokens: u.CacheCreation.Ephemeral1hInputTokens,
		CacheReadTokens:    u.CacheReadInputTokens,
		ThinkingTokens:     u.OutputTokensDetails.ThinkingTokens,
		WebSearchRequests:  u.ServerToolUse.WebSearchRequests,
		WebFetchRequests:   u.ServerToolUse.WebFetchRequests,
	}
	// Бэкенд, отдающий только лумпованный total (прокси, совместимый API), иначе
	// потерял бы cache-write и в usage, и в стоимости.
	if out.CacheWrite5mTokens+out.CacheWrite1hTokens == 0 {
		out.CacheWrite5mTokens = u.CacheCreationInputTokens
	}
	return out
}

// mapDeltaUsage — usage из message_delta стрима. Cache-write split здесь
// недоступен: лумпованный total относим к 5m-ставке, авторитетный usage — из
// Stream.Final.
func mapDeltaUsage(u anthropic.MessageDeltaUsage) Usage {
	return Usage{
		InputTokens:        u.InputTokens,
		OutputTokens:       u.OutputTokens,
		CacheWrite5mTokens: u.CacheCreationInputTokens,
		CacheReadTokens:    u.CacheReadInputTokens,
		ThinkingTokens:     u.OutputTokensDetails.ThinkingTokens,
		WebSearchRequests:  u.ServerToolUse.WebSearchRequests,
		WebFetchRequests:   u.ServerToolUse.WebFetchRequests,
	}
}
