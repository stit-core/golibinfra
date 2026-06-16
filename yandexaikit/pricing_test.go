package yandexaikit

import "testing"

func TestModelKey(t *testing.T) {
	cases := map[string]string{
		"yandexgpt":                           "yandexgpt",
		"yandexgpt/latest":                    "yandexgpt",
		"gpt://b1g/yandexgpt/latest":          "yandexgpt",
		"emb://b1g/text-search-doc/latest":    "text-search-doc",
		"art://b1g/yandex-art/latest":         "yandex-art",
		"gpt://b1g/yandexgpt-lite/latest@sfx": "yandexgpt-lite",
	}
	for in, want := range cases {
		if got := modelKey(in); got != want {
			t.Errorf("modelKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCostSyncKnown(t *testing.T) {
	// Pro 5 sync: ₽1.20/1k токенов = 1_200_000_000 micros/MTok.
	// 1M input + 1M output @ ₽1.20/1k = ₽1200 + ₽1200 = ₽2400 = 2_400_000_000 micros.
	got, known := Cost("yandexgpt", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if !known {
		t.Fatal("expected known model")
	}
	if got != 2_400_000_000 {
		t.Fatalf("cost = %d, want 2400000000", got)
	}
}

func TestCostViaModelURI(t *testing.T) {
	// modelURI-форма должна резолвиться к тому же ключу.
	got, known := Cost("gpt://b1g/yandexgpt/latest", Usage{InputTokens: 1_000_000})
	if !known || got != 1_200_000_000 {
		t.Fatalf("cost = %d known=%v, want 1200000000 true", got, known)
	}
}

func TestCostAsyncCheaper(t *testing.T) {
	sync, _ := Cost("yandexgpt", Usage{InputTokens: 1_000_000})
	async, known := CostAsync("yandexgpt", Usage{InputTokens: 1_000_000})
	if !known {
		t.Fatal("expected known async model")
	}
	if async >= sync {
		t.Fatalf("async %d should be < sync %d", async, sync)
	}
}

func TestCostImage(t *testing.T) {
	// YandexART: ₽2.23/кадр = 2_230_000 micros.
	got, known := Cost("yandex-art", Usage{Images: 2})
	if !known || got != 4_460_000 {
		t.Fatalf("cost = %d known=%v, want 4460000 true", got, known)
	}
}

func TestCostUnknown(t *testing.T) {
	if _, known := Cost("nonexistent", Usage{InputTokens: 100}); known {
		t.Fatal("expected unknown model")
	}
}
