package googleaikit

import (
	"testing"

	"google.golang.org/genai"
)

func TestMapUsage(t *testing.T) {
	u := mapUsage(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:        100,
		CachedContentTokenCount: 30,
		CandidatesTokenCount:    50,
		ThoughtsTokenCount:      20,
		ToolUsePromptTokenCount: 5,
	})
	// promptTokenCount ВКЛЮЧАЕТ cached ⇒ InputTokens = 100, CachedTokens = 30 (подмножество).
	if u.InputTokens != 100 || u.CachedTokens != 30 || u.OutputTokens != 50 || u.ReasoningTokens != 20 || u.ToolUseTokens != 5 {
		t.Fatalf("mapUsage = %+v", u)
	}
}

func TestMapUsageNil(t *testing.T) {
	if u := mapUsage(nil); u != (Usage{}) {
		t.Fatalf("mapUsage(nil) = %+v, want zero", u)
	}
}

func TestUsageTotalTokens(t *testing.T) {
	// Cached/Reasoning — подмножества, в Total не добавляются.
	u := Usage{InputTokens: 100, OutputTokens: 50, ToolUseTokens: 5, CachedTokens: 30, ReasoningTokens: 20}
	if got := u.TotalTokens(); got != 155 {
		t.Fatalf("TotalTokens() = %d, want 155", got)
	}
}
