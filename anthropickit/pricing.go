package anthropickit

import "strings"

// USDMicros — деньги целочисленно: микро-доллары (1 USD = 1_000_000). Float для
// денег не используем. Все ставки таблицы — в USDMicros за 1M токенов (per-MTok),
// web-search — за 1000 запросов.
type USDMicros int64

// Price — ставки одной модели. Стоимость считается с усечением вниз по каждой
// компоненте и не знает про batch, fast mode и тарифные ярусы длинного контекста
// (docs/configuration.md § Что Cost не учитывает).
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

const (
	usd              USDMicros = 1_000_000
	webSearchPerKReq           = 10 * usd
)

// tier — ставка модели по цене входа/выхода/чтения кэша; запись кэша по правилу
// Anthropic: 5m = 1.25× входа, 1h = 2× входа.
func tier(input, output, cacheRead USDMicros) Price {
	return Price{
		InputPerMTok:        input,
		OutputPerMTok:       output,
		CacheWrite5mPerMTok: input * 5 / 4,
		CacheWrite1hPerMTok: input * 2,
		CacheReadPerMTok:    cacheRead,
		WebSearchPerKReq:    webSearchPerKReq,
	}
}

// defaultPrices — прайс Anthropic first-party API по алиасам моделей; датированный
// snapshot-id (…-YYYYMMDD) резолвится в алиас в priceFor. Меняется при смене
// прайсов Anthropic (апдейт кита + релиз); наценка/новые модели до апдейта — через
// WithPriceTable на стороне потребителя.
var defaultPrices = map[string]Price{
	"claude-fable-5-1":  tier(10*usd, 50*usd, usd/4), // cache-read 0.025× входа — исключение из правила 0.1×
	"claude-fable-5":    tier(10*usd, 50*usd, usd),
	"claude-mythos-5":   tier(10*usd, 50*usd, usd), // тариф Fable 5; доступ — Project Glasswing
	"claude-opus-5":     tier(5*usd, 25*usd, usd/2),
	"claude-opus-4-8":   tier(5*usd, 25*usd, usd/2),
	"claude-opus-4-7":   tier(5*usd, 25*usd, usd/2),
	"claude-opus-4-6":   tier(5*usd, 25*usd, usd/2),
	"claude-sonnet-5":   tier(2*usd, 10*usd, usd/5),
	"claude-sonnet-4-6": tier(3*usd, 15*usd, 3*usd/10),
	"claude-haiku-4-5":  tier(usd, 5*usd, usd/10),
}

// priceFor ищет модель по точному имени, затем без датированного суффикса: API
// возвращает snapshot-id вроде claude-haiku-4-5-20251001.
func priceFor(prices map[string]Price, model string) (Price, bool) {
	if p, ok := prices[model]; ok {
		return p, true
	}
	if alias := stripSnapshotDate(model); alias != model {
		p, ok := prices[alias]
		return p, ok
	}
	return Price{}, false
}

func stripSnapshotDate(model string) string {
	i := strings.LastIndexByte(model, '-')
	if i < 0 || len(model)-i-1 != 8 {
		return model
	}
	for _, c := range model[i+1:] {
		if c < '0' || c > '9' {
			return model
		}
	}
	return model[:i]
}

// Cost — стоимость по канонной таблице; known=false для неизвестной модели
// (стоимость 0, fallback решает потребитель). Для своей таблицы — WithPriceTable.
func Cost(model string, u Usage) (cost USDMicros, known bool) {
	p, ok := priceFor(defaultPrices, model)
	if !ok {
		return 0, false
	}
	return p.cost(u), true
}
