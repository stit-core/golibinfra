package sberaikit

// Usage — нормализованный учёт токенов одного запроса GigaChat. Нормализация по
// docs/domains/_shared/accounting-ports.md: prompt_tokens→InputTokens (полный
// вход, cached уже включён), completion_tokens→OutputTokens,
// precached_prompt_tokens→CachedTokens (подмножество InputTokens). SystemTokens —
// иногда присутствующая системная надбавка; Extra удерживает неизвестные ключи
// usage, чтобы биллинг не терялся на schema-bump провайдера.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	TotalTokens  int64
	SystemTokens int64
	Extra        map[string]int64
}

// rawUsage — wire-форма usage GigaChat (chat и embeddings). Известные поля —
// явно; всё прочее ловим в Extra через двойной unmarshal в decode-пути.
type rawUsage struct {
	PromptTokens          int64 `json:"prompt_tokens"`
	CompletionTokens      int64 `json:"completion_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
	PrecachedPromptTokens int64 `json:"precached_prompt_tokens"`
	SystemTokens          int64 `json:"system_tokens"`
}

func (r rawUsage) normalize() Usage {
	total := r.TotalTokens
	if total == 0 {
		total = r.PromptTokens + r.CompletionTokens
	}
	return Usage{
		InputTokens:  r.PromptTokens,
		OutputTokens: r.CompletionTokens,
		CachedTokens: r.PrecachedPromptTokens,
		TotalTokens:  total,
		SystemTokens: r.SystemTokens,
	}
}

// addExtra регистрирует неизвестный целочисленный usage-ключ (forward-совместимость
// биллинга при добавлении провайдером новых счётчиков).
func (u *Usage) addExtra(key string, v int64) {
	if u.Extra == nil {
		u.Extra = make(map[string]int64, 1)
	}
	u.Extra[key] = v
}
