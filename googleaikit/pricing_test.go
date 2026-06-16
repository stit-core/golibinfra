package googleaikit

import "testing"

func TestCostTextWithCache(t *testing.T) {
	// gemini-2.5-flash: in $0.30/MTok, out $2.50/MTok, cached-read $0.03/MTok.
	// 1M input (200k cached) + 1M output:
	//   uncached 800k @ 0.30 = 240_000; cached 200k @ 0.03 = 6_000; out 1M @ 2.50 = 2_500_000.
	got, known := Cost("gemini-2.5-flash", Usage{InputTokens: 1_000_000, CachedTokens: 200_000, OutputTokens: 1_000_000})
	if !known {
		t.Fatal("expected known model")
	}
	if want := USDMicros(240_000 + 6_000 + 2_500_000); got != want {
		t.Fatalf("cost = %d, want %d", got, want)
	}
}

func TestCostReasoningIncludedInOutput(t *testing.T) {
	// ReasoningTokens ⊆ OutputTokens: цена считается по OutputTokens, reasoning не добавляет.
	a, _ := Cost("gemini-2.5-flash", Usage{OutputTokens: 1_000_000})
	b, _ := Cost("gemini-2.5-flash", Usage{OutputTokens: 1_000_000, ReasoningTokens: 500_000})
	if a != b {
		t.Fatalf("reasoning double-counted: %d != %d", a, b)
	}
}

func TestCostImage(t *testing.T) {
	// imagen-4.0: $0.04/image → 40_000 micros; 2 картинки = 80_000.
	got, _ := Cost("imagen-4.0-generate-001", Usage{Images: 2})
	if got != 80_000 {
		t.Fatalf("cost = %d, want 80000", got)
	}
}

func TestCostVideoPerSecond(t *testing.T) {
	// veo-3.1: $0.40/s → 400_000 micros; 8s = 3_200_000.
	got, _ := Cost("veo-3.1-generate-preview", Usage{AudioSeconds: 8})
	if got != 3_200_000 {
		t.Fatalf("cost = %d, want 3200000", got)
	}
}

func TestCostUnknownModel(t *testing.T) {
	if _, known := Cost("nonexistent", Usage{InputTokens: 100}); known {
		t.Fatal("expected unknown model")
	}
}
