package yandexaikit

// RUBMicros — деньги целочисленно: микро-рубли (1 RUB = 1_000_000). Float для
// денег не используем. Валюта Yandex Cloud — RUB (см. accounting-ports.md: cost
// несёт валюту провайдера как есть). Ставки таблицы — в RUBMicros за 1M токенов
// (per-MTok); image — за один сгенерированный кадр.
//
// Async-тариф Yandex ≈ ½ от sync (research §8). Прайс async-вызовов задаётся
// отдельной таблицей (defaultPricesAsync); Cost/CostAsync выбирают нужную.
type RUBMicros int64

type Price struct {
	InputPerMTok  RUBMicros
	OutputPerMTok RUBMicros
	ImagePerUnit  RUBMicros // YandexART: за один кадр
}

func (p Price) cost(u Usage) RUBMicros {
	perMTok := func(tok int64, rate RUBMicros) RUBMicros {
		return RUBMicros(tok) * rate / 1_000_000
	}
	return perMTok(u.InputTokens, p.InputPerMTok) +
		perMTok(u.OutputTokens, p.OutputPerMTok) +
		RUBMicros(u.Images)*p.ImagePerUnit
}

// modelKey нормализует адрес модели к ключу прайс-таблицы. Принимает как bare
// model-id ("yandexgpt"), так и modelUri ("gpt://<folder>/yandexgpt/latest") —
// извлекает <model>-сегмент, отбрасывая schema, folder и version.
func modelKey(model string) string {
	s := model
	if i := indexScheme(s); i >= 0 {
		s = s[i+3:] // после "://"
		// "<folder>/<model>/<version>" → <model>
		s = afterFirstSlash(s)
		s = beforeFirstSlash(s)
	}
	// версия на bare-форме ("yandexgpt/latest") — отбрасываем
	return beforeFirstSlash(s)
}

func indexScheme(s string) int {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == ':' && s[i+1] == '/' && s[i+2] == '/' {
			return i
		}
	}
	return -1
}

func beforeFirstSlash(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i]
		}
	}
	return s
}

func afterFirstSlash(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[i+1:]
		}
	}
	return s
}

// defaultPrices — sync-ставки Yandex AI Studio (RUB incl. VAT → RUBMicros/MTok),
// по research §8 (per-1000-tokens × 1000). Меняются при смене прайсов Yandex
// (апдейт кита + релиз). Наценку/новые модели — через WithPriceTable у потребителя.
var defaultPrices = map[string]Price{
	// YandexGPT Pro 5 (`yandexgpt` /latest): ₽1.20 / 1k токенов (all).
	"yandexgpt": {InputPerMTok: 1_200_000_000, OutputPerMTok: 1_200_000_000},
	// YandexGPT Lite (`yandexgpt-lite`): ₽0.20 / 1k токенов (all).
	"yandexgpt-lite": {InputPerMTok: 200_000_000, OutputPerMTok: 200_000_000},
	// Embeddings (`text-search-doc`/`-query`): ₽0.0101 / 1k токенов.
	"text-search-doc":   {InputPerMTok: 10_100_000},
	"text-search-query": {InputPerMTok: 10_100_000},
	// YandexART (`yandex-art`): ₽2.23 / кадр.
	"yandex-art": {ImagePerUnit: 2_230_000},
}

// defaultPricesAsync — async-тарифы (≈½ sync), для completionAsync. Только
// текстовые модели имеют отдельный async-тариф; image всегда async (per-image).
var defaultPricesAsync = map[string]Price{
	// YandexGPT Pro 5 async: ₽0.61 / 1k токенов (in/out).
	"yandexgpt": {InputPerMTok: 610_000_000, OutputPerMTok: 610_000_000},
	// YandexGPT Lite async: ₽0.10 / 1k токенов.
	"yandexgpt-lite": {InputPerMTok: 100_000_000, OutputPerMTok: 100_000_000},
}

// Cost — sync-стоимость по канонной таблице. known=false для неизвестной модели:
// гейтвей сам решает fallback. model — bare-id или modelUri (любой). Для своей
// таблицы — WithPriceTable + Client.
func Cost(model string, u Usage) (cost RUBMicros, known bool) {
	p, ok := defaultPrices[modelKey(model)]
	if !ok {
		return 0, false
	}
	return p.cost(u), true
}

// CostAsync — async-стоимость (тариф ≈½). Для completionAsync.
func CostAsync(model string, u Usage) (cost RUBMicros, known bool) {
	p, ok := defaultPricesAsync[modelKey(model)]
	if !ok {
		return 0, false
	}
	return p.cost(u), true
}
