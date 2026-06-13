package claudekit

// USDMicros — деньги целочисленно: микро-доллары (1 USD = 1_000_000). Float для
// денег не используем. Все ставки таблицы — в USDMicros за 1M токенов (per-MTok),
// web-search — за 1000 запросов.
type USDMicros int64

type Price struct {
	InputPerMTok        USDMicros
	OutputPerMTok       USDMicros
	CacheWrite5mPerMTok USDMicros
	CacheWrite1hPerMTok USDMicros
	CacheReadPerMTok    USDMicros
	WebSearchPerKReq    USDMicros
}

func (p Price) cost(u Usage) USDMicros {
	perMTok := func(tok int64, rate USDMicros) USDMicros {
		return USDMicros(tok) * rate / 1_000_000
	}
	c := perMTok(u.InputTokens, p.InputPerMTok) +
		perMTok(u.OutputTokens, p.OutputPerMTok) +
		perMTok(u.CacheWrite5mTokens, p.CacheWrite5mPerMTok) +
		perMTok(u.CacheWrite1hTokens, p.CacheWrite1hPerMTok) +
		perMTok(u.CacheReadTokens, p.CacheReadPerMTok)
	c += USDMicros(u.WebSearchRequests) * p.WebSearchPerKReq / 1000
	return c
}

// defaultPrices — канонные цены Anthropic ($/MTok → USDMicros). Меняются при
// смене прайсов Anthropic (апдейт кита + релиз). Наценку/новые модели до апдейта
// — через WithPriceTable на стороне потребителя.
var defaultPrices = map[string]Price{
	"claude-opus-4-8": {
		InputPerMTok: 5_000_000, OutputPerMTok: 25_000_000,
		CacheWrite5mPerMTok: 6_250_000, CacheWrite1hPerMTok: 10_000_000,
		CacheReadPerMTok: 500_000, WebSearchPerKReq: 10_000_000,
	},
	"claude-opus-4-7": {
		InputPerMTok: 5_000_000, OutputPerMTok: 25_000_000,
		CacheWrite5mPerMTok: 6_250_000, CacheWrite1hPerMTok: 10_000_000,
		CacheReadPerMTok: 500_000, WebSearchPerKReq: 10_000_000,
	},
	"claude-sonnet-4-6": {
		InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000,
		CacheWrite5mPerMTok: 3_750_000, CacheWrite1hPerMTok: 6_000_000,
		CacheReadPerMTok: 300_000, WebSearchPerKReq: 10_000_000,
	},
	"claude-haiku-4-5": {
		InputPerMTok: 1_000_000, OutputPerMTok: 5_000_000,
		CacheWrite5mPerMTok: 1_250_000, CacheWrite1hPerMTok: 2_000_000,
		CacheReadPerMTok: 100_000, WebSearchPerKReq: 10_000_000,
	},
	"claude-fable-5": {
		InputPerMTok: 10_000_000, OutputPerMTok: 50_000_000,
		CacheWrite5mPerMTok: 12_500_000, CacheWrite1hPerMTok: 20_000_000,
		CacheReadPerMTok: 1_000_000, WebSearchPerKReq: 10_000_000,
	},
}

// Cost — стоимость по канонной таблице. known=false для неизвестной модели:
// гейтвей сам решает fallback. Для своей таблицы — WithPriceTable + Client.
func Cost(model string, u Usage) (cost USDMicros, known bool) {
	p, ok := defaultPrices[model]
	if !ok {
		return 0, false
	}
	return p.cost(u), true
}
