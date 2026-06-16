package yandexaikit

import (
	"encoding/json"
	"testing"
)

func TestUsageFromContent(t *testing.T) {
	c := ContentUsage{
		InputTextTokens:         21,
		CompletionTokens:        17,
		TotalTokens:             38,
		CompletionTokensDetails: &CompletionTokensDetails{ReasoningTokens: 5},
	}
	u := usageFromContent(c)
	if u.InputTokens != 21 || u.OutputTokens != 17 || u.ReasoningTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
	if u.TotalTokens() != 38 {
		t.Fatalf("TotalTokens() = %d, want 38", u.TotalTokens())
	}
}

// int64-as-string: Yandex отдаёт usage-токены строками; `,string`-теги парсят.
func TestContentUsageInt64AsString(t *testing.T) {
	raw := `{"inputTextTokens":"21","completionTokens":"17","totalTokens":"38","completionTokensDetails":{"reasoningTokens":"5"}}`
	var c ContentUsage
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.InputTextTokens != 21 || c.CompletionTokens != 17 || c.TotalTokens != 38 {
		t.Fatalf("parsed = %+v", c)
	}
	if c.CompletionTokensDetails == nil || c.CompletionTokensDetails.ReasoningTokens != 5 {
		t.Fatalf("reasoning details = %+v", c.CompletionTokensDetails)
	}
}

func TestEmbeddingNumTokensAsString(t *testing.T) {
	raw := `{"embedding":[0.1,0.2],"numTokens":"8","modelVersion":"06.12.2023"}`
	var e EmbeddingResponse
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.NumTokens != 8 {
		t.Fatalf("numTokens = %d, want 8", e.NumTokens)
	}
}
