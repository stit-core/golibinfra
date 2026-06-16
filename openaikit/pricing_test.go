package openaikit

import "testing"

func TestCostKnownModel(t *testing.T) {
	// 1M input @ $5/MTok = $5; 1M output @ $30/MTok = $30; итого $35 = 35_000_000 micros.
	got, known := Cost("gpt-5.5", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if !known {
		t.Fatal("expected known model")
	}
	if got != 35_000_000 {
		t.Fatalf("cost = %d, want 35000000", got)
	}
}

func TestCostCachedNotDoubleCounted(t *testing.T) {
	// 1M input из которых 1M cached: uncached=0 @ $5 = 0; cached 1M @ $0.50 = 500_000.
	got, _ := Cost("gpt-5.5", Usage{InputTokens: 1_000_000, CachedTokens: 1_000_000})
	if got != 500_000 {
		t.Fatalf("cost = %d, want 500000 (cached-only, no double count)", got)
	}
}

func TestCostAudioDuration(t *testing.T) {
	// whisper-1: 60s @ 100 µ$/sec = 6000 µ$ = $0.006/min.
	got, _ := Cost("whisper-1", Usage{AudioSeconds: 60})
	if got != 6000 {
		t.Fatalf("cost = %d, want 6000", got)
	}
}

func TestCostUnknownModel(t *testing.T) {
	if _, known := Cost("nonexistent-model", Usage{InputTokens: 100}); known {
		t.Fatal("expected unknown model")
	}
}
