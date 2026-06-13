package claudekit

import "testing"

func TestCostKnownModel(t *testing.T) {
	// 1M input @ $5/MTok = $5; 1M output @ $25/MTok = $25; итого $30 = 30_000_000 micros.
	got, known := Cost("claude-opus-4-8", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if !known {
		t.Fatal("expected known model")
	}
	if got != 30_000_000 {
		t.Fatalf("cost = %d, want 30000000", got)
	}
}

func TestCostCacheAndWebSearch(t *testing.T) {
	// cache-read 1M @ $0.50/MTok = 500_000; web_search 1000 req @ $10/1000 = 10_000_000.
	got, _ := Cost("claude-opus-4-8", Usage{CacheReadTokens: 1_000_000, WebSearchRequests: 1000})
	if got != 10_500_000 {
		t.Fatalf("cost = %d, want 10500000", got)
	}
}

func TestCostUnknownModel(t *testing.T) {
	if _, known := Cost("nonexistent-model", Usage{InputTokens: 100}); known {
		t.Fatal("expected unknown model")
	}
}
