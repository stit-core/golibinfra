package anthropickit

import "net/http"

// Option — функциональная опция конструктора. Отдельно от Config: Config — это
// env-контракт, Option — программная настройка (кастомный HTTP-клиент,
// прайс-таблица с наценкой, переопределение base URL в тестах).
type Option func(*options)

type options struct {
	httpClient *http.Client
	prices     map[string]Price
	baseURL    string // "" → берётся из Config
}

// WithHTTPClient подменяет HTTP-клиент SDK (таймауты/транспорт/тест-сервер).
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.httpClient = c }
}

// WithPriceTable переопределяет прайс-таблицу целиком (наценка, новые модели до
// апдейта кита). Передаётся как есть, не мерджится с дефолтной.
func WithPriceTable(prices map[string]Price) Option {
	return func(o *options) { o.prices = prices }
}

// WithBaseURL переопределяет base URL поверх Config (тесты против stub-сервера).
func WithBaseURL(url string) Option {
	return func(o *options) { o.baseURL = url }
}
