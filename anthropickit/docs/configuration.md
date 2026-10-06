# Конфигурация anthropickit

## Config (ENV)

`anthropickit.Config` — публичный env-контракт. Загружается через
config-композицию (ENV) либо напрямую `anthropickit.NewConfig()`
(ENV ридером китов + `Validate`).

> **Public API.** Имена полей и их `envconfig`/`default`/`required`-теги —
> часть стабильного контракта. Менять их = breaking change: выпускается
> minor-релизом, помечается `!` в Conventional Commit и строкой миграции в
> аннотации тега (см. [корневой README § Версионирование](../../README.md)).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `ANTHROPIC_API_KEY` | `APIKey` | `string` | — (`required`) | Ключ Anthropic API. Пустой → `ErrConfig`. |
| `ANTHROPIC_BASE_URL` | `BaseURL` | `string` | `https://api.anthropic.com` | Хост Anthropic API. «Шов» под split-топологию / прокси. Не абсолютный `http(s)://host` → `ErrConfig`. Можно перекрыть опцией `WithBaseURL` (валидируется так же). |
| `ANTHROPIC_DEFAULT_MODEL` | `DefaultModel` | `string` | `claude-opus-5` | Модель при пустом `Request.Model`; её же проверяет health-проба. Пустая → `ErrConfig`. |
| `ANTHROPIC_MAX_RETRIES` | `MaxRetries` | `int` | `2` | Число повторов SDK при 408/409/429/5xx, сетевых ошибках и таймауте попытки (экспоненциальный backoff, `Retry-After` учитывается). `0` — ровно одна попытка. `< 0` → `ErrConfig`. |
| `ANTHROPIC_TIMEOUT` | `Timeout` | `time.Duration` | `10m` | Таймаут **одной попытки** (`option.WithRequestTimeout`): для `Complete`/`CountTokens` — весь запрос-ответ, для `Stream` — от запроса до конца чтения SSE (дедлайн живёт, пока читается тело). Попытка, упёршаяся в таймаут, ретраится, поэтому wall-clock одного вызова доходит до `Timeout × (MaxRetries + 1)` плюс backoff; общий бюджет задаёт `ctx` вызова. `<= 0` → `ErrConfig`. |

`Config.Validate()` проверяет всё перечисленное; `New` вызывает его сам, поэтому
неработоспособный конфиг падает на старте, а не на первом запросе.

**Config — единственный источник endpoint'а и учётных данных.** Кит создаёт
SDK-клиент с `option.WithoutEnvironmentDefaults()`: переменные окружения самого
SDK (ANTHROPIC_AUTH_TOKEN, ANTHROPIC_PROFILE, профили в `~/.config/anthropic`,
federation-переменные, ANTHROPIC_CUSTOM_HEADERS) не читаются и заголовок
`Authorization` не добавляется. `ANTHROPIC_API_KEY`/`ANTHROPIC_BASE_URL`
попадают в клиент только через `Config` (то есть через `NewConfig`/App-конфиг).

Длинные стриминговые генерации: дефолтные 10 минут режут стрим целиком, поэтому
под ответы, которые заведомо генерируются дольше, поднимайте `ANTHROPIC_TIMEOUT`
явно.

## Опции конструктора (`Option`)

Программная настройка, не выражаемая через ENV. Применяются в `New(cfg, log, opts...)`.

| Опция | Назначение |
|---|---|
| `WithHTTPClient(*http.Client)` | Базовый HTTP-клиент SDK (транспорт, cookie, тест-сервер). Его транспорт кит оборачивает в `otelhttp.NewTransport`; без опции — клон `http.DefaultTransport` с `ResponseHeaderTimeout` 10 мин. |
| `WithPriceTable(map[string]Price)` | Переопределяет прайс-таблицу целиком (наценка, новые модели до апдейта кита). Передаётся как есть, **не мерджится** с `defaultPrices`. `nil` → дефолтная таблица. |
| `WithBaseURL(string)` | Перекрывает base URL поверх `Config.BaseURL` (тесты против stub-сервера). Пустая строка → значение из `Config`; непустая проходит ту же валидацию. |

## Логгер

`New(cfg, log, ...)` принимает `logkit.Logger` (`nil` → `logkit.Nop()`); что и на
каком уровне пишется — в [observability.md](observability.md) § Логи.

## Дефолты и потолок `max_tokens`

Если `Request.MaxTokens == 0`, кит подставляет дефолт в зависимости от режима;
явный `Request.MaxTokens > 0` его перекрывает.

| Режим | Дефолт | Потолок | Почему |
|---|---|---|---|
| `Complete` (sync) | `16_000` | `21_333` | API отдаёт ответ темпом ~128K токенов в час, так что больший `max_tokens` не укладывается в 10 минут — практический предел одного HTTP-ответа. |
| `Stream` | `64_000` | нет | Таймауты SSE не грозят — даём модели простор. |

`Complete` с `MaxTokens` выше потолка возвращает `ErrMaxTokensRequiresStreaming`
**до** HTTP-вызова: метрика и спан не пишутся (вызова не было), потребитель
переключается на `Stream`.

Потолок фиксированный и от `ANTHROPIC_TIMEOUT` не зависит: поднятый таймаут его
не отменяет, единственный путь к большим ответам — `Stream` (там потолка нет).

## Прайс-таблица по умолчанию

`defaultPrices` (в `pricing.go`) — цены Anthropic first-party API, ставки в
`USDMicros` за 1M токенов (web-search — за 1000 запросов). Запись кэша считается
по правилу Anthropic: 5m = 1.25× входа, 1h = 2× входа; чтение кэша — 0.1× входа
(у Fable 5.1 — 0.025×).

| Модель | Input $/MTok | Output $/MTok | Cache read $/MTok |
|---|---|---|---|
| `claude-fable-5-1` | 10 | 50 | 0.25 |
| `claude-fable-5` | 10 | 50 | 1.00 |
| `claude-mythos-5` | 10 | 50 | 1.00 |
| `claude-opus-5` | 5 | 25 | 0.50 |
| `claude-opus-4-8` | 5 | 25 | 0.50 |
| `claude-opus-4-7` | 5 | 25 | 0.50 |
| `claude-opus-4-6` | 5 | 25 | 0.50 |
| `claude-sonnet-5` | 2 | 10 | 0.20 |
| `claude-sonnet-4-6` | 3 | 15 | 0.30 |
| `claude-haiku-4-5` | 1 | 5 | 0.10 |

Web-search у всех моделей — $10 за 1000 запросов. Ключи таблицы — алиасы;
snapshot-id из ответа API (`claude-haiku-4-5-20251001`) резолвится в алиас.
Таблица в коде и таблица выше сверяются тестом (`TestDefaultPricesMatchDocs`),
так что расходиться молча они не могут.

`claude-mythos-5` доступен только участникам Project Glasswing и тарифицируется
как `claude-fable-5` — тариф у них общий.

Модель вне таблицы → `Cost=0`, `CostKnown=false` (свободная `Cost` возвращает
`known=false`), в метриках `priced="false"` и `model="other"`. Так живут модели,
публичной ставки которых у кита нет: `claude-mythos-5-1` (ставка чтения кэша не
опубликована) и legacy вроде `claude-opus-4-5`/`claude-sonnet-4-5`. Тарифицируйте
их через `WithPriceTable`.

Таблица обновляется при смене прайсов Anthropic (правка кита + релиз). Для своей
таблицы (наценка / неизвестная модель) — `WithPriceTable` на стороне потребителя.

### Что `Cost` не учитывает

`Price.cost` — плоский расчёт по ставкам таблицы. Мимо него проходят:

- **Округление.** Каждая компонента (вход, выход, запись/чтение кэша,
  web-search) считается целочисленно с усечением вниз — систематический
  недоучёт до нескольких микро-долларов на запрос.
- **Batch API** (−50 %) и **fast mode** (Opus 5 по $10/$50 за MTok) — оба
  доступны только через `Raw()`, ставка в таблице одна.
- **Long-context** и прочие тарифные ярусы: ставка считается плоской для всего
  окна каждой модели.
- **`service_tier`/`speed`** свободной функции `Cost` неизвестны — она знает
  только модель и `Usage`.

Если потребитель ходит в API через `Raw()` в этих режимах, стоимость он считает
сам.
