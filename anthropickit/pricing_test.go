package anthropickit

import "testing"

func TestCostKnownModel(t *testing.T) {
	// 1M input @ $5/MTok = $5; 1M output @ $25/MTok = $25; итого $30 = 30_000_000 micros.
	got, known := Cost("claude-opus-5", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
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

func TestCostSpotChecks(t *testing.T) {
	cases := []struct {
		model string
		usage Usage
		want  USDMicros
	}{
		// Fable 5.1: $10 input, cache-read $0.25 (0.025×), не 0.1×.
		{"claude-fable-5-1", Usage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000}, 10_250_000},
		// Fable 5: тот же тариф, но cache-read $1 (0.1×).
		{"claude-fable-5", Usage{CacheReadTokens: 1_000_000}, 1_000_000},
		// Sonnet 5: $2/$10; cache-write 1h = 2× входа = $4.
		{"claude-sonnet-5", Usage{InputTokens: 500_000, OutputTokens: 100_000, CacheWrite1hTokens: 1_000_000}, 1_000_000 + 1_000_000 + 4_000_000},
		// Sonnet 4.6: $3/$15; cache-write 5m = 1.25× = $3.75.
		{"claude-sonnet-4-6", Usage{CacheWrite5mTokens: 1_000_000, OutputTokens: 1_000_000}, 3_750_000 + 15_000_000},
		// Haiku 4.5: $1/$5; cache-read $0.10.
		{"claude-haiku-4-5", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}, 6_100_000},
		// Opus 4.6/4.7 — тариф Opus.
		{"claude-opus-4-7", Usage{InputTokens: 200_000}, 1_000_000},
		{"claude-opus-4-6", Usage{OutputTokens: 40_000}, 1_000_000},
	}
	for _, tc := range cases {
		got, known := Cost(tc.model, tc.usage)
		if !known {
			t.Errorf("%s: expected known", tc.model)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: cost = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestDefaultPricesFollowCacheMultipliers(t *testing.T) {
	for model, p := range defaultPrices {
		if p.InputPerMTok <= 0 || p.OutputPerMTok <= p.InputPerMTok {
			t.Errorf("%s: input/output = %d/%d", model, p.InputPerMTok, p.OutputPerMTok)
		}
		if p.CacheWrite5mPerMTok != p.InputPerMTok*5/4 {
			t.Errorf("%s: cache-write 5m = %d, want 1.25× input", model, p.CacheWrite5mPerMTok)
		}
		if p.CacheWrite1hPerMTok != p.InputPerMTok*2 {
			t.Errorf("%s: cache-write 1h = %d, want 2× input", model, p.CacheWrite1hPerMTok)
		}
		if p.CacheReadPerMTok <= 0 || p.CacheReadPerMTok > p.InputPerMTok/10 {
			t.Errorf("%s: cache-read = %d, want (0, 0.1× input]", model, p.CacheReadPerMTok)
		}
		if p.WebSearchPerKReq != 10_000_000 {
			t.Errorf("%s: web-search = %d, want $10/1000", model, p.WebSearchPerKReq)
		}
	}
	for _, model := range []string{"claude-fable-5-1", "claude-fable-5", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-4-5"} {
		if _, ok := defaultPrices[model]; !ok {
			t.Errorf("%s missing from defaultPrices", model)
		}
	}
}

func TestCostResolvesSnapshotID(t *testing.T) {
	alias, _ := Cost("claude-haiku-4-5", Usage{InputTokens: 10_000})
	dated, known := Cost("claude-haiku-4-5-20251001", Usage{InputTokens: 10_000})
	if !known || dated != alias {
		t.Fatalf("snapshot id: cost = %d known=%v, want %d", dated, known, alias)
	}
	if _, known := Cost("claude-haiku-4-5-2025", Usage{InputTokens: 1}); known {
		t.Fatal("non-date suffix must not resolve")
	}
}

func TestCostUnknownModel(t *testing.T) {
	cost, known := Cost("nonexistent-model", Usage{InputTokens: 100})
	if known || cost != 0 {
		t.Fatalf("expected unknown model with zero cost, got %d known=%v", cost, known)
	}
}
