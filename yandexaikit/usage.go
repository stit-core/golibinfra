package yandexaikit

// Usage — нормализованный учёт одного запроса. Поля сверены с контрактом
// _shared/accounting-ports.md (Consumption): InputTokens — полный вход,
// ReasoningTokens ⊆ OutputTokens. Image-генерация без токенов: Images=1.
//
// Источник на проводе — int64 как JSON-строки (proto-JSON rule), парсятся в
// wire-структурах (`,string`-теги в types.go), сюда приходят уже числами.
type Usage struct {
	InputTokens     int64 // inputTextTokens (промпт)
	OutputTokens    int64 // completionTokens (сгенерированные)
	ReasoningTokens int64 // completionTokensDetails.reasoningTokens ⊆ OutputTokens
	Images          int64 // YandexART: 1 per-request (без токенов)
}

// TotalTokens — суммарные токены запроса (input + output).
func (u Usage) TotalTokens() int64 { return u.InputTokens + u.OutputTokens }

// usageFromContent нормализует ContentUsage (completion) → Usage.
// totalTokens из ответа игнорируем как производное (input+output) — не дублируем
// источник правды, считаем сами при нужде.
func usageFromContent(c ContentUsage) Usage {
	u := Usage{
		InputTokens:  c.InputTextTokens,
		OutputTokens: c.CompletionTokens,
	}
	if c.CompletionTokensDetails != nil {
		u.ReasoningTokens = c.CompletionTokensDetails.ReasoningTokens
	}
	return u
}
