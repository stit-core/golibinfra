package openaikit

import (
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func TestMapChatUsage(t *testing.T) {
	u := mapChatUsage(openai.CompletionUsage{
		PromptTokens:            100,
		CompletionTokens:        40,
		PromptTokensDetails:     openai.CompletionUsagePromptTokensDetails{CachedTokens: 30},
		CompletionTokensDetails: openai.CompletionUsageCompletionTokensDetails{ReasoningTokens: 10},
	})
	if u.InputTokens != 100 || u.OutputTokens != 40 || u.CachedTokens != 30 || u.ReasoningTokens != 10 {
		t.Fatalf("chat usage mapping = %+v", u)
	}
	if u.Modality != ModalityText {
		t.Fatalf("modality = %q", u.Modality)
	}
}

func TestMapResponsesUsage(t *testing.T) {
	u := mapResponsesUsage(responses.ResponseUsage{
		InputTokens:         200,
		OutputTokens:        50,
		InputTokensDetails:  responses.ResponseUsageInputTokensDetails{CachedTokens: 80},
		OutputTokensDetails: responses.ResponseUsageOutputTokensDetails{ReasoningTokens: 25},
	})
	if u.InputTokens != 200 || u.OutputTokens != 50 || u.CachedTokens != 80 || u.ReasoningTokens != 25 {
		t.Fatalf("responses usage mapping = %+v", u)
	}
}
