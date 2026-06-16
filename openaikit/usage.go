package openaikit

import (
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

// Modality — категория потребления одного вызова (для метеринга/прайсинга).
type Modality string

const (
	ModalityText      Modality = "text"
	ModalityEmbedding Modality = "embedding"
	ModalityImage     Modality = "image"
	ModalityAudio     Modality = "audio"
)

// Usage — нормализованный учёт одного запроса по контракту accounting-ports:
// единый тип поверх двух usage-форм OpenAI (Chat prompt_tokens/completion_tokens
// vs Responses input_tokens/output_tokens) и не-токенных модальностей.
// Инварианты: CachedTokens ⊆ InputTokens; ReasoningTokens ⊆ OutputTokens — поэтому
// прайсинг вычитает подмножества, чтобы не считать дважды.
type Usage struct {
	Modality Modality

	InputTokens     int64 // полный вход (cached уже включён)
	OutputTokens    int64 // сгенерированные (reasoning уже включён)
	CachedTokens    int64 // подмножество InputTokens — прочитано из prompt-кэша (дешевле)
	ReasoningTokens int64 // подмножество OutputTokens — reasoning/thinking (биллится как output)

	Images       int     // сгенерированных/обработанных изображений
	AudioSeconds float64 // STT/TTS длительность (для duration-биллинга, напр. whisper-1)
}

// mapChatUsage нормализует Chat Completions usage. prompt_tokens/input уже включает
// cached; reasoning_tokens — подмножество completion_tokens.
func mapChatUsage(u openai.CompletionUsage) Usage {
	return Usage{
		Modality:        ModalityText,
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		CachedTokens:    u.PromptTokensDetails.CachedTokens,
		ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
	}
}

// mapResponsesUsage нормализует Responses usage. input_tokens уже включает cached;
// reasoning_tokens — подмножество output_tokens.
func mapResponsesUsage(u responses.ResponseUsage) Usage {
	return Usage{
		Modality:        ModalityText,
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CachedTokens:    u.InputTokensDetails.CachedTokens,
		ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
	}
}

// mapEmbeddingUsage — embeddings отдаёт только prompt_tokens (весь вход).
func mapEmbeddingUsage(u openai.CreateEmbeddingResponseUsage) Usage {
	return Usage{Modality: ModalityEmbedding, InputTokens: u.PromptTokens}
}

// mapImageUsage — gpt-image usage токенами (input text+image, output image-токены).
// Images проставляется числом отданных картинок отдельно (по len(data)).
func mapImageUsage(u openai.ImagesResponseUsage, images int) Usage {
	return Usage{
		Modality:     ModalityImage,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		Images:       images,
	}
}
