package openaikit

// USDMicros — деньги целочисленно: микро-доллары (1 USD = 1_000_000). Float для
// денег не используем. Токен-ставки таблицы — в USDMicros за 1M токенов (per-MTok);
// audio-ставки — за 1 секунду (для duration-биллинга, напр. whisper-1).
type USDMicros int64

type Price struct {
	InputPerMTok  USDMicros // некэшированный + кэшированный вход (cached считается отдельно)
	CachedPerMTok USDMicros // ставка для cached-read подмножества входа
	OutputPerMTok USDMicros // выход (включая reasoning-токены)
	AudioPerSec   USDMicros // duration-биллинг STT (whisper-1 и пр.)
}

// cost считает стоимость по нормализованному Usage. Cached-токены вычитаются из
// входа (CachedTokens ⊆ InputTokens) и тарифицируются по cached-ставке отдельно —
// без двойного счёта. Reasoning уже учтён внутри OutputTokens (та же ставка).
func (p Price) cost(u Usage) USDMicros {
	perMTok := func(tok int64, rate USDMicros) USDMicros {
		return USDMicros(tok) * rate / 1_000_000
	}
	uncachedInput := u.InputTokens - u.CachedTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	c := perMTok(uncachedInput, p.InputPerMTok) +
		perMTok(u.CachedTokens, p.CachedPerMTok) +
		perMTok(u.OutputTokens, p.OutputPerMTok)
	if p.AudioPerSec > 0 {
		c += USDMicros(u.AudioSeconds * float64(p.AudioPerSec))
	}
	return c
}

// defaultPrices — канонные цены OpenAI ($/MTok → USDMicros), сверены с
// research/providers/openai.md §8 (2026-06-14). Меняются при смене прайсов OpenAI
// (апдейт кита + релиз). Наценку/новые модели до апдейта — через WithPriceTable.
// CachedPerMTok=0 там, где cached-ставка не опубликована/неприменима. whisper-1 —
// duration-биллинг ($0.006/min = 100 µ$/sec).
var defaultPrices = map[string]Price{
	// Flagship / reasoning chat (GPT-5.x).
	"gpt-5.5":      {InputPerMTok: 5_000_000, CachedPerMTok: 500_000, OutputPerMTok: 30_000_000},
	"gpt-5.5-pro":  {InputPerMTok: 30_000_000, OutputPerMTok: 180_000_000},
	"gpt-5.4":      {InputPerMTok: 2_500_000, CachedPerMTok: 250_000, OutputPerMTok: 15_000_000},
	"gpt-5.4-mini": {InputPerMTok: 750_000, CachedPerMTok: 75_000, OutputPerMTok: 4_500_000},
	"gpt-5.4-nano": {InputPerMTok: 200_000, CachedPerMTok: 20_000, OutputPerMTok: 1_250_000},
	"gpt-5.4-pro":  {InputPerMTok: 30_000_000, OutputPerMTok: 180_000_000},

	// o-series reasoning.
	"o3-pro":                {InputPerMTok: 20_000_000, OutputPerMTok: 80_000_000},
	"o3-mini":               {InputPerMTok: 1_100_000, CachedPerMTok: 550_000, OutputPerMTok: 4_400_000},
	"o4-mini":               {InputPerMTok: 1_100_000, OutputPerMTok: 4_400_000},
	"o1":                    {InputPerMTok: 15_000_000, CachedPerMTok: 7_500_000, OutputPerMTok: 60_000_000},
	"o3-deep-research":      {InputPerMTok: 5_000_000, OutputPerMTok: 20_000_000},
	"o4-mini-deep-research": {InputPerMTok: 1_000_000, OutputPerMTok: 4_000_000},

	// GPT-4.x / 4o.
	"gpt-4.1":      {InputPerMTok: 2_000_000, CachedPerMTok: 500_000, OutputPerMTok: 8_000_000},
	"gpt-4.1-nano": {InputPerMTok: 100_000},
	"gpt-4o-mini":  {InputPerMTok: 150_000, CachedPerMTok: 75_000, OutputPerMTok: 600_000},

	// Images (gpt-image-2): per-token прайсинг (text-in / cached / out).
	"gpt-image-2": {InputPerMTok: 8_000_000, CachedPerMTok: 2_000_000, OutputPerMTok: 30_000_000},

	// Audio STT/TTS.
	"gpt-4o-transcribe":      {InputPerMTok: 2_500_000, OutputPerMTok: 10_000_000},
	"gpt-4o-mini-transcribe": {InputPerMTok: 1_250_000, OutputPerMTok: 5_000_000},
	"whisper-1":              {AudioPerSec: 100}, // $0.006/min
	"gpt-4o-mini-tts":        {InputPerMTok: 600_000, OutputPerMTok: 12_000_000},
	"tts-1":                  {InputPerMTok: 15_000_000}, // $15/1M chars (учёт по символам входа)
	"tts-1-hd":               {InputPerMTok: 30_000_000},

	// Embeddings.
	"text-embedding-3-small": {InputPerMTok: 20_000},
	"text-embedding-3-large": {InputPerMTok: 130_000},
	"text-embedding-ada-002": {InputPerMTok: 100_000},
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
