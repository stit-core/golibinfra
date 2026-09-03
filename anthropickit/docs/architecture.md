# Архитектура anthropickit

## Назначение и место в стеке

`anthropickit` — тонкий, но не «прозрачный» слой между бизнес-кодом сервиса и
`anthropic-sdk-go`. Он решает три задачи, которые иначе пришлось бы решать в
каждом сервисе заново:

1. **Декаплинг от SDK.** Потребитель работает с нативными типами кита
   (`Request`/`Response`/`Block`/`Usage`), а не с union-параметрами SDK. Это даёт
   стабильную поверхность для DI и моков (интерфейс `API`) и изолирует сервис от
   эволюции SDK.
2. **Нормализованный учёт.** `Usage` приводит разрозненные поля SDK
   (cache-creation по TTL, server-tool use, thinking-токены) к одной плоской
   структуре; `Cost` считает стоимость целочисленно в микро-долларах по
   прайс-таблице, `CostKnown` говорит, была ли модель в таблице.
3. **Observability-by-default.** Каждый вызов эмитит метрики calls/latency/
   tokens/cost через otel-go SDK, каждый HTTP-запрос к API идёт через
   otelhttp-транспорт (клиентский спан), сбои пишутся в `logkit.Logger`.

БД у кита нет: хранение запросов/ответов, леджер usage и любые агрегации —
ответственность сервиса.

## Компоненты

| Файл | Роль |
|---|---|
| `anthropickit.go` | `API`-интерфейс, `Client`, конструктор `New` (otelhttp-транспорт, retry-логирующий middleware SDK, таймаут/ретраи), `Stream`, `Raw`, `costOf`, `observe`/`logFailure` — единая точка завершения вызова. |
| `config.go` | `Config` — публичный env-контракт, `Validate`. |
| `messages.go` | Нативные типы (`Request`/`Response`/`Block`/`Message`/`ToolDef`), `Complete`, `CountTokens`, маппинг `toParams`/`blockToParam`/`toTools`/`toToolChoice`, `buildResponse`. |
| `stream.go` | `Stream`-интерфейс, `sseStream` (поверх `ssestream.Stream`), `errStream`, нормализация событий (`translate`), однократная запись метрик в `finish` (исчерпание SSE или `Close`). |
| `usage.go` | `Usage`, `mapUsage` (из `anthropic.Usage`), `mapDeltaUsage` (из `message_delta`). |
| `pricing.go` | `USDMicros`, `Price`, `defaultPrices`, `tier`, `priceFor` (алиас + snapshot-id), `Price.cost`, свободная функция `Cost`. |
| `metrics.go` | `instruments` (4 otel-инструмента через `internal/otelkit`), `callResult`, `record`. |
| `errors.go` | Sentinel-ошибки `Err*`, `classify` (HTTP-статус → sentinel). |
| `options.go` | Функциональные опции `WithHTTPClient`/`WithPriceTable`/`WithBaseURL`. |
| `module.go` | fx `Module`, `NewConfig` (envconfig + `Validate`), `newClient`. |
| `testkit/anthropictest/` | Тест-двойники: `Stub`, `ScriptStream`, `ErrStream`. |

## Поток данных

### Синхронный вызов (`Complete`)

```
Request (нативный)
   │  toParams: model←Config.DefaultModel при ""; max_tokens←16_000 при 0;
   │            blockToParam по каждому блоку; thinking adaptive|disabled;
   │            effort/JSONSchema → OutputConfig
   ▼
anthropic.MessageNewParams ──► sdk.Messages.New (otelhttp-спан, ретраи SDK)
   │                              │ ошибка → observe(status=error, warn-лог) → classify → Err*
   ▼                              ▼
anthropic.Message ── buildResponse ──► Response (Content, StopReason, Usage, Cost, CostKnown)
   │                                       │ costOf(model, Usage) по прайс-таблице
   ▼
observe(status=ok, latency, Usage, Cost)
```

### Стриминговый вызов (`Stream`)

`Stream` выполняет HTTP-запрос сразу (SDK открывает SSE в `NewStreaming`) и
возвращает итератор; `Next` читает события. Латентность меряется от момента
перед запросом. Если маппинг запроса упал ещё до HTTP-вызова — возвращается
`errStream` (`Next()=false`, ошибка через `Err()`/`Final()`, метрик нет —
вызова API не было).

```
Stream() ──► sdk.Messages.NewStreaming (HTTP-запрос уходит здесь)
Next() ──► s.Next() (SSE) ──► acc.Accumulate(ev) ──► translate(ev) → StreamEvent
                                   │ ошибка аккумуляции → err, finish()
   когда SSE исчерпан/ошибка ──► finish(): однократно observe(...) по acc.Usage
Close() до конца SSE ──► finish(status=abandoned) с накопленным usage/cost
Final() ──► buildResponse(&acc, model) → Response (авторитетные Usage+Cost)
```

Дефолт `max_tokens` для стрима — `64_000` (таймауты SSE не грозят, даём модели
простор), для sync — `16_000` (держим под HTTP-таймаутами SDK).

### Предполётный подсчёт (`CountTokens`)

`sdk.Messages.CountTokens` с теми же `Messages`/`System`/`Tools`/`Thinking`/
`ToolChoice`; `observe(op=count_tokens)` пишет calls/latency, но не токены —
подсчёт не тарифицируется.

## Ключевые проектные решения (WHY)

- **`Config` vs `Option`.** `Config` — это env-контракт (грузится из ENV,
  имена тегов = public API). `Option` — программная настройка, которая в ENV не
  выражается: кастомный `*http.Client`, прайс-таблица с наценкой,
  override base URL для тестов. Поэтому base URL живёт и в `Config.BaseURL`, и в
  `WithBaseURL` — опция перекрывает Config.
- **Свой HTTP-клиент с otelhttp.** SDK спанов не ведёт, поэтому кит всегда
  передаёт в SDK `*http.Client`, чей транспорт обёрнут в `otelhttp.NewTransport`
  (клиентский спан на каждый HTTP-вызов, trace-context уходит в API). Базовый
  транспорт — клон `http.DefaultTransport` с `ResponseHeaderTimeout` 10 мин
  (как у дефолта SDK: молчащий сервер не держит запрос вечно; тело стрима не
  ограничивает) либо транспорт клиента из `WithHTTPClient`.
- **Ретраи и таймаут — у SDK.** `ANTHROPIC_MAX_RETRIES` передаётся в SDK как
  есть (`0` — ровно одна попытка), `ANTHROPIC_TIMEOUT` — таймаут одной попытки
  (`option.WithRequestTimeout`); для стрима его дедлайн живёт до конца чтения
  SSE, поэтому для длинных генераций он либо `0`, либо с запасом; ctx вызова
  ограничивает всё вместе. Повторные попытки SDK видны в логе на `Debug` через
  middleware по заголовку `X-Stainless-Retry-Count`.
- **Refusal — не ошибка.** Отказ safety-классификатора Claude приходит как
  успешный HTTP 200 со `StopReason=refusal`. Кит не превращает его в `error`:
  `Response.IsRefusal()` — явная проверка на стороне потребителя. Sentinel-ошибки
  `Err*` — только транспортные/протокольные сбои API.
- **Деньги целочисленно.** `USDMicros` (1 USD = 1_000_000) вместо float —
  суммирование без накопления ошибки. Ставки прайс-таблицы — per-MTok (за 1M
  токенов), web-search — за 1000 запросов.
- **Прайс-таблица — встроенная, но переопределяемая.** `defaultPrices` хранит
  канонные цены Anthropic по алиасам моделей; snapshot-id (`…-YYYYMMDD`) из
  ответа API резолвится в алиас. Наценка, или новая модель до апдейта кита —
  через `WithPriceTable` (передаётся целиком, не мерджится). Неизвестная модель →
  `Cost=0`, `CostKnown=false`, метрика `priced=false`; fallback решает
  потребитель.
- **Усечённый `buildResponse`.** В `Response.Content` маппятся только
  text/tool_use/thinking блоки. Остальные блоки SDK (server-tool use и т.п.) в
  нативный ответ не попадают — для них есть `Raw()`.
- **`Raw()` как escape hatch.** Managed Agents (`Raw().Beta.Agents`/`Sessions`),
  batches, files и прочее, что кит не покрывает нативно, доступно напрямую через
  SDK-клиент. Это сознательный trade-off: кит покрывает Messages-поверхность, не
  пытаясь обернуть весь SDK.
- **Авторитетный Usage стрима — из `Final`.** В событии `message_delta` usage
  кумулятивен, но cache-write split по TTL там недоступен (только лумпованный
  total). Точный финальный `Usage` (с разбивкой 5m/1h) берётся из
  аккумулированного `Message.Usage` в `Stream.Final`.
- **Брошенный стрим тоже учитывается.** `Close` до исчерпания SSE пишет метрики
  со `status=abandoned` и накопленным usage/cost — иначе прерванные генерации
  выпадали бы из учёта, хотя API их тарифицирует.
- **fx `Module` не provide'ит `Config`.** Симметрично с остальными китами
  (asymmetry note в `pgkit/module.go`): `Config` собирает приложение. Дешёвый
  путь — `fx.Provide(anthropickit.NewConfig)`. Опции (`WithPriceTable` и т.п.) —
  через свой конструктор поверх `New`.
