package openaikit

import "github.com/openai/openai-go/v3/option"

// Option — функциональная опция конструктора. Отдельно от Config: Config — это
// env-контракт, Option — программная настройка (кастомный HTTP-клиент, прайс-таблица
// с наценкой, override base URL/доп-заголовки в тестах и под Azure).
type Option func(*options)

type options struct {
	httpClient option.HTTPClient
	prices     map[string]Price
	baseURL    string                 // "" → берётся из Config
	extra      []option.RequestOption // доп. SDK-опции (Azure api-key scheme, api-version и т.п.)
}

// WithHTTPClient подменяет HTTP-клиент SDK (таймауты/транспорт/тест-сервер).
// Принимает любой option.HTTPClient (метод Do) — в т.ч. golibinfra/httpclient.Client.
func WithHTTPClient(c option.HTTPClient) Option {
	return func(o *options) { o.httpClient = c }
}

// WithPriceTable переопределяет прайс-таблицу целиком (наценка, новые модели до
// апдейта кита). Передаётся как есть, не мерджится с дефолтной.
func WithPriceTable(prices map[string]Price) Option {
	return func(o *options) { o.prices = prices }
}

// WithBaseURL переопределяет base URL поверх Config (тесты против stub-сервера,
// Azure-эндпоинт).
func WithBaseURL(url string) Option {
	return func(o *options) { o.baseURL = url }
}

// WithRequestOptions добавляет произвольные SDK-опции (Azure api-key auth-scheme,
// api-version query, доп. заголовки). Эскейп-хэтч под не выраженное в Config.
func WithRequestOptions(opts ...option.RequestOption) Option {
	return func(o *options) { o.extra = append(o.extra, opts...) }
}
