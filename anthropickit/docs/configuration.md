# Конфигурация anthropickit

## Config (ENV)

`anthropickit.Config` — публичный env-контракт. Загружается через
config-композицию (ENV) либо напрямую `anthropickit.NewConfig()`
(`envconfig.Process` + `Validate`).

> **Public API.** Имена полей и их `envconfig`/`default`/`required`-теги —
> часть стабильного контракта. Менять их = breaking change, идёт мажорным
> апгрейдом (см. [корневой README § Стабильность API](../../README.md)).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `ANTHROPIC_API_KEY` | `APIKey` | `string` | — (`required`) | Ключ Anthropic API. Без него `Validate`/`New` возвращают `ErrConfig`. |
| `ANTHROPIC_BASE_URL` | `BaseURL` | `string` | `https://api.anthropic.com` | Хост Anthropic API. «Шов» под split-топологию / прокси. Можно перекрыть опцией `WithBaseURL`. |
| `ANTHROPIC_DEFAULT_MODEL` | `DefaultModel` | `string` | `claude-opus-4-8` | Модель по умолчанию при пустом `Request.Model`. |
| `ANTHROPIC_MAX_RETRIES` | `MaxRetries` | `int` | `2` | Число повторов SDK при 408/409/429/5xx и сетевых ошибках (экспоненциальный backoff, `Retry-After` учитывается). `0` — ровно одна попытка, ретраев нет. `< 0` → `ErrConfig`. |
| `ANTHROPIC_TIMEOUT` | `Timeout` | `time.Duration` | `0` | Таймаут **одной попытки** (`option.WithRequestTimeout`): для `Complete`/`CountTokens` — весь запрос-ответ, для `Stream` — от запроса до конца чтения SSE (дедлайн живёт, пока читается тело). `0` — дефолт SDK: 10 мин на sync-запрос, стрим без дедлайна (только `ResponseHeaderTimeout` транспорта на ожидание заголовков). Общий бюджет на все попытки задаёт `ctx` вызова. `< 0` → `ErrConfig`. |

`Config.Validate()` проверяет всё перечисленное; `New` вызывает его сам.

## Опции конструктора (`Option`)

Программная настройка, не выражаемая через ENV. Применяются в `New(cfg, log, opts...)`.

| Опция | Назначение |
|---|---|
| `WithHTTPClient(*http.Client)` | Базовый HTTP-клиент SDK (транспорт, cookie, тест-сервер). Его транспорт кит оборачивает в `otelhttp.NewTransport`; без опции — клон `http.DefaultTransport` с `ResponseHeaderTimeout` 10 мин. |
| `WithPriceTable(map[string]Price)` | Переопределяет прайс-таблицу целиком (наценка, новые модели до апдейта кита). Передаётся как есть, **не мерджится** с `defaultPrices`. `nil` → дефолтная таблица. |
| `WithBaseURL(string)` | Перекрывает base URL поверх `Config.BaseURL` (тесты против stub-сервера). Пустая строка → значение из `Config`. |

## Логгер

`New(cfg, log, ...)` принимает `logkit.Logger` (`nil` → `logkit.Nop()`); что и на
каком уровне пишется — в [observability.md](observability.md) § Логи.

## Дефолты `max_tokens`

Если `Request.MaxTokens == 0`, кит подставляет дефолт в зависимости от режима:

| Режим | Дефолт | Почему |
|---|---|---|
| `Complete` (sync) | `16_000` | Держим ответ под HTTP-таймаутами SDK. |
| `Stream` | `64_000` | Таймауты SSE не грозят — даём модели простор. |

Явный `Request.MaxTokens > 0` всегда перекрывает дефолт.

## Прайс-таблица по умолчанию

`defaultPrices` (в `pricing.go`) — цены Anthropic first-party API, ставки в
`USDMicros` за 1M токенов (web-search — за 1000 запросов). Запись кэша считается
по правилу Anthropic: 5m = 1.25× входа, 1h = 2× входа; чтение кэша — 0.1× входа
(у Fable 5.1 — 0.025×).

| Модель | Input $/MTok | Output $/MTok | Cache read $/MTok |
|---|---|---|---|
| `claude-fable-5-1` | 10 | 50 | 0.25 |
| `claude-fable-5` | 10 | 50 | 1.00 |
| `claude-opus-5` | 5 | 25 | 0.50 |
| `claude-opus-4-8` | 5 | 25 | 0.50 |
| `claude-opus-4-7` | 5 | 25 | 0.50 |
| `claude-opus-4-6` | 5 | 25 | 0.50 |
| `claude-sonnet-5` | 2 | 10 | 0.20 |
| `claude-sonnet-4-6` | 3 | 15 | 0.30 |
| `claude-haiku-4-5` | 1 | 5 | 0.10 |

Web-search у всех моделей — $10 за 1000 запросов. Ключи таблицы — алиасы;
snapshot-id из ответа API (`claude-haiku-4-5-20251001`) резолвится в алиас.
Модель вне таблицы → `Cost=0`, `CostKnown=false` (свободная `Cost` возвращает
`known=false`), метрика `anthropic_api_calls_total{priced="false"}`.

Таблица обновляется при смене прайсов Anthropic (правка кита + релиз). Для своей
таблицы (наценка / неизвестная модель) — `WithPriceTable` на стороне потребителя.
