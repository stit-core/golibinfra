# Конфигурация anthropickit

## Config (ENV)

`anthropickit.Config` — публичный env-контракт. Загружается через
config-композицию (ENV + Vault) либо напрямую `anthropickit.NewConfig()`
(`envconfig.Process`).

> **Public API.** Имена полей и их `envconfig`/`vault`/`default`/`required`-теги —
> часть стабильного контракта. Менять их = breaking change, идёт мажорным
> апгрейдом (см. [корневой README § Стабильность API](../../README.md)).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `ANTHROPIC_API_KEY` | `APIKey` | `string` | — (`required`, `vault:"anthropic_api_key"`) | Ключ Anthropic API. Без него `New` возвращает `ErrConfig`. |
| `ANTHROPIC_BASE_URL` | `BaseURL` | `string` | `https://api.anthropic.com` | Хост Anthropic API. «Шов» под split-топологию / прокси. Можно перекрыть опцией `WithBaseURL`. |
| `ANTHROPIC_DEFAULT_MODEL` | `DefaultModel` | `string` | `claude-opus-4-8` | Модель по умолчанию при пустом `Request.Model`. |
| `ANTHROPIC_MAX_RETRIES` | `MaxRetries` | `int` | `2` | Максимум ретраев SDK (передаётся в `option.WithMaxRetries`, только при `> 0`). SDK ретраит 429/5xx с экспоненциальным backoff. |

## Опции конструктора (`Option`)

Программная настройка, не выражаемая через ENV. Применяются в `New(cfg, log, opts...)`.

| Опция | Назначение |
|---|---|
| `WithHTTPClient(*http.Client)` | Подменяет HTTP-клиент SDK (таймауты, транспорт, тест-сервер). |
| `WithPriceTable(map[string]Price)` | Переопределяет прайс-таблицу целиком (наценка, новые модели до апдейта кита). Передаётся как есть, **не мерджится** с `defaultPrices`. `nil` → дефолтная таблица. |
| `WithBaseURL(string)` | Перекрывает base URL поверх `Config.BaseURL` (тесты против stub-сервера). Пустая строка → значение из `Config`. |

## Дефолты `max_tokens`

Если `Request.MaxTokens == 0`, кит подставляет дефолт в зависимости от режима:

| Режим | Дефолт | Почему |
|---|---|---|
| `Complete` (sync) | `16_000` | Держим ответ под HTTP-таймаутами SDK. |
| `Stream` | `64_000` | Таймауты SSE не грозят — даём модели простор. |

Явный `Request.MaxTokens > 0` всегда перекрывает дефолт.

## Прайс-таблица по умолчанию

`defaultPrices` (в `pricing.go`) — канонные цены Anthropic, ставки в `USDMicros`
за 1M токенов (web-search — за 1000 запросов). Модели в таблице:
`claude-opus-4-8`, `claude-opus-4-7`, `claude-sonnet-4-6`, `claude-haiku-4-5`,
`claude-fable-5`. Каждая `Price` несёт: `InputPerMTok`, `OutputPerMTok`,
`CacheWrite5mPerMTok`, `CacheWrite1hPerMTok`, `CacheReadPerMTok`,
`WebSearchPerKReq`.

Таблица обновляется при смене прайсов Anthropic (правка кита + релиз). Для своей
таблицы (наценка / неизвестная модель) — `WithPriceTable` на стороне потребителя.
