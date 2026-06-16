package googleaikit

import "google.golang.org/genai"

// Usage — нормализованный учёт одного запроса по контракту accounting-ports
// (docs/domains/_shared/accounting-ports.md). Инварианты без двойного счёта:
// CachedTokens ⊆ InputTokens, ReasoningTokens ⊆ OutputTokens.
//
// Маппинг genai.UsageMetadata: promptTokenCount (ВКЛЮЧАЕТ cached) → InputTokens;
// cachedContentTokenCount → CachedTokens; candidatesTokenCount → OutputTokens;
// thoughtsTokenCount → ReasoningTokens. Не-токенные единицы (Images/AudioSeconds)
// заполняют image/video/audio-пути, где usageMetadata нет.
type Usage struct {
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64 // ⊆ InputTokens — прочитано из context-cache
	ReasoningTokens int64 // ⊆ OutputTokens — thoughtsTokenCount (биллится как output)
	ToolUseTokens   int64 // tool-use prompt-токены (отдельный счётчик usageMetadata)

	Images       int64   // сгенерированных изображений (image-путь)
	AudioSeconds float64 // длительность TTS/видео (audio/video-путь)
}

// TotalTokens — полный объём токенов запроса (вход + выход + tool-use), как в
// usageMetadata.totalTokenCount. Cached/Reasoning не добавляются — они подмножества.
func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens + u.ToolUseTokens
}

// mapUsage нормализует genai.UsageMetadata. promptTokenCount уже включает cached,
// поэтому InputTokens берётся как есть, а CachedTokens — подмножество.
func mapUsage(m *genai.GenerateContentResponseUsageMetadata) Usage {
	if m == nil {
		return Usage{}
	}
	return Usage{
		InputTokens:     int64(m.PromptTokenCount),
		OutputTokens:    int64(m.CandidatesTokenCount),
		CachedTokens:    int64(m.CachedContentTokenCount),
		ReasoningTokens: int64(m.ThoughtsTokenCount),
		ToolUseTokens:   int64(m.ToolUsePromptTokenCount),
	}
}
