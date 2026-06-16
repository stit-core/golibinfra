package sberaikit

// RUBMicros — деньги целочисленно: микро-рубли (1 RUB = 1_000_000). Float для
// денег не используем. Валюта GigaChat — RUB (см. accounting-ports.md § Cost).
// Все ставки таблицы — в RUBMicros за 1M токенов (per-MTok). Cost считается по
// total_tokens (тариф Sber умножает именно его), без раздельных input/output
// ставок: публичный CORP-прайс — единая ставка за 1000 токенов на модель.
type RUBMicros int64

// CurrencyRUB — валюта стоимости GigaChat для нормализованного Cost потребителя.
const CurrencyRUB = "RUB"

type Price struct {
	PerMTok RUBMicros // ставка за 1M токенов (total)
}

func (p Price) cost(u Usage) RUBMicros {
	return RUBMicros(u.TotalTokens) * p.PerMTok / 1_000_000
}

// defaultPrices — CORP pay-as-you-go ставки Sber (research §8: ₽ за 1000 токенов
// → RUBMicros за 1M токенов, т.е. ×1000×1_000_000/1000 = ×1_000_000). GigaChat
// (Lite) 0.065₽/1k → 65_000 микро-₽/1k → 65_000_000 микро-₽/1M. Меняются при
// смене прайсов Sber (апдейт кита + релиз; Sber ревизовал в фев 2026). Наценку/
// новые модели до апдейта — через WithPriceTable на стороне потребителя.
var defaultPrices = map[string]Price{
	"GigaChat-2":     {PerMTok: 65_000_000},  // 0.065 ₽ / 1k
	"GigaChat-2-Pro": {PerMTok: 500_000_000}, // 0.5 ₽ / 1k
	"GigaChat-2-Max": {PerMTok: 650_000_000}, // 0.65 ₽ / 1k

	"Embeddings":                {PerMTok: 14_000_000}, // 0.014 ₽ / 1k
	"Embeddings-2":              {PerMTok: 14_000_000},
	"EmbeddingsGigaR":           {PerMTok: 14_000_000},
	"GigaEmbeddings-3B-2025-09": {PerMTok: 14_000_000},
}

// Cost — стоимость по канонной таблице (RUB). known=false для неизвестной модели:
// гейтвей сам решает fallback. Для своей таблицы — WithPriceTable + Client.
func Cost(model string, u Usage) (cost RUBMicros, known bool) {
	p, ok := defaultPrices[model]
	if !ok {
		return 0, false
	}
	return p.cost(u), true
}
