package googleaikit

// USDMicros — деньги целочисленно: микро-доллары (1 USD = 1_000_000). Float для
// денег не используем. Токен-ставки таблицы — в USDMicros за 1M токенов (per-MTok);
// image — за изображение; video/audio — за секунду.
type USDMicros int64

// Price — ставки одной модели. Поля, нерелевантные модальности модели, = 0
// (text-модель: ImagePerImage/VideoPerSecond == 0; image-модель: только
// ImagePerImage и опционально InputPerMTok для Nano-Banana токен-входа).
type Price struct {
	InputPerMTok      USDMicros // некэшированный + cached вход (по PromptTokenCount)
	OutputPerMTok     USDMicros // выход, включая reasoning/thoughts
	CachedReadPerMTok USDMicros // cached-read; вычитается из входа (cached дешевле)
	ImagePerImage     USDMicros // за сгенерированное изображение
	VideoPerSecond    USDMicros // за секунду видео (Veo)
	AudioPerSecond    USDMicros // за секунду аудио-выхода (TTS)
}

func (p Price) cost(u Usage) USDMicros {
	perMTok := func(tok int64, rate USDMicros) USDMicros {
		return USDMicros(tok) * rate / 1_000_000
	}
	// Cached-read биллится по своей (более низкой) ставке; некэшированный вход —
	// остаток InputTokens за вычетом cached.
	uncachedInput := u.InputTokens - u.CachedTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	c := perMTok(uncachedInput, p.InputPerMTok) +
		perMTok(u.CachedTokens, p.CachedReadPerMTok) +
		perMTok(u.OutputTokens, p.OutputPerMTok)
	c += USDMicros(u.Images) * p.ImagePerImage
	c += USDMicros(u.AudioSeconds * float64(p.VideoPerSecond))
	c += USDMicros(u.AudioSeconds * float64(p.AudioPerSecond))
	return c
}

// defaultPrices — канонные цены Google Gemini (Developer-API paid tier,
// research/providers/google.md §8, USD → USDMicros). Pro-модели имеют контекстное
// тиринг (>200k); таблица несёт базовую (≤200k) ставку — наценку/тиринг до апдейта
// кита потребитель задаёт через WithPriceTable. Vertex list-цены те же per-token.
// VideoPerSecond у Veo — Standard 720p/1080p (4K тарифицируется через WithPriceTable).
var defaultPrices = map[string]Price{
	// Text — Gemini 3.x / 2.5 (input / output per MTok).
	"gemini-3.1-pro-preview": {InputPerMTok: 2_000_000, OutputPerMTok: 12_000_000, CachedReadPerMTok: 200_000},
	"gemini-3.5-flash":       {InputPerMTok: 1_500_000, OutputPerMTok: 9_000_000, CachedReadPerMTok: 150_000},
	"gemini-3-flash-preview": {InputPerMTok: 500_000, OutputPerMTok: 3_000_000, CachedReadPerMTok: 50_000},
	"gemini-3.1-flash-lite":  {InputPerMTok: 250_000, OutputPerMTok: 1_500_000, CachedReadPerMTok: 25_000},
	"gemini-2.5-pro":         {InputPerMTok: 1_250_000, OutputPerMTok: 10_000_000, CachedReadPerMTok: 125_000},
	"gemini-2.5-flash":       {InputPerMTok: 300_000, OutputPerMTok: 2_500_000, CachedReadPerMTok: 30_000},
	"gemini-2.5-flash-lite":  {InputPerMTok: 100_000, OutputPerMTok: 400_000, CachedReadPerMTok: 10_000},

	// Embeddings (input per MTok).
	"gemini-embedding-2":   {InputPerMTok: 200_000},
	"gemini-embedding-001": {InputPerMTok: 150_000},

	// Image — Nano Banana (input-токены + per-image) и Imagen (per-image only).
	"gemini-2.5-flash-image":  {InputPerMTok: 300_000, ImagePerImage: 39_000},
	"gemini-3.1-flash-image":  {InputPerMTok: 500_000, ImagePerImage: 45_000},
	"gemini-3-pro-image":      {ImagePerImage: 134_000},
	"imagen-4.0-generate-001": {ImagePerImage: 40_000},

	// Video — Veo (per second, Standard 720p/1080p).
	"veo-3.1-generate-preview": {VideoPerSecond: 400_000},
	"veo-3.0-generate-001":     {VideoPerSecond: 400_000},
	"veo-2.0-generate-001":     {VideoPerSecond: 350_000},

	// Audio / TTS (input text per MTok + output audio per MTok; per-second не
	// публикуется — биллинг по выходным audio-токенам, см. usageMetadata).
	"gemini-2.5-flash-preview-tts": {InputPerMTok: 500_000, OutputPerMTok: 10_000_000},
	"gemini-3.1-flash-tts-preview": {InputPerMTok: 1_000_000, OutputPerMTok: 20_000_000},
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
