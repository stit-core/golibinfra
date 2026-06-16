package yandexaikit

import "time"

// Option — функциональная опция конструктора. Отдельно от Config: Config — это
// env-контракт, Option — программная настройка (прайс-таблица с наценкой,
// poll-политика, base URL в тестах).
type Option func(*options)

type options struct {
	prices      map[string]Price
	pricesAsync map[string]Price
	baseURL     string // "" → берётся из Config
	poll        pollConfig
}

// pollConfig — политика поллинга async-операций (clockkit-интервалы, бэкофф).
type pollConfig struct {
	Initial    time.Duration // первый интервал ожидания
	Max        time.Duration // потолок интервала после бэкоффа
	Multiplier float64       // множитель экспоненциального бэкоффа
	Timeout    time.Duration // суммарный бюджет ожидания done (0 → только ctx)
}

// defaultPollConfig — YandexART операции занимают секунды–минуты; стартуем с 1s,
// растём до 5s, общий бюджет 5 минут (ctx может урезать раньше).
var defaultPollConfig = pollConfig{
	Initial:    time.Second,
	Max:        5 * time.Second,
	Multiplier: 1.6,
	Timeout:    5 * time.Minute,
}

// WithPriceTable переопределяет sync-прайс целиком (наценка, новые модели до
// апдейта кита). Передаётся как есть, не мерджится с дефолтной.
func WithPriceTable(prices map[string]Price) Option {
	return func(o *options) { o.prices = prices }
}

// WithAsyncPriceTable переопределяет async-прайс целиком.
func WithAsyncPriceTable(prices map[string]Price) Option {
	return func(o *options) { o.pricesAsync = prices }
}

// WithBaseURL переопределяет base URL поверх Config (тесты против stub-сервера).
func WithBaseURL(url string) Option {
	return func(o *options) { o.baseURL = url }
}

// WithPollConfig переопределяет политику поллинга async-операций.
func WithPollConfig(p pollConfig) Option {
	return func(o *options) { o.poll = p }
}
