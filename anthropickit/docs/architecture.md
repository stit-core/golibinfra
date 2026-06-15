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
   прайс-таблице.
3. **Observability-by-default.** Каждый вызов эмитит метрики calls/latency/
   tokens/cost через otel-go SDK без участия потребителя.

БД у кита нет: хранение запросов/ответов, леджер usage и любые агрегации —
ответственность сервиса.

## Компоненты

| Файл | Роль |
|---|---|
| `anthropickit.go` | `API`-интерфейс, `Client`, конструктор `New`, `Stream`, `Raw`, `costOf`. |
| `config.go` | `Config` — публичный env-контракт. |
| `messages.go` | Нативные типы (`Request`/`Response`/`Block`/`Message`/`ToolDef`), `Complete`, `CountTokens`, маппинг `toParams`/`blockToParam`/`toTools`/`toToolChoice`, `buildResponse`. |
| `stream.go` | `Stream`-интерфейс, `sseStream` (поверх `ssestream.Stream`), `errStream`, нормализация событий (`translate`), однократная запись метрик в `finish`. |
| `usage.go` | `Usage`, `mapUsage` (из `anthropic.Usage`), `mapDeltaUsage` (из `message_delta`). |
| `pricing.go` | `USDMicros`, `Price`, `defaultPrices`, `Price.cost`, свободная функция `Cost`. |
| `metrics.go` | `instruments` (4 otel-инструмента), `newInstruments`, `record`. |
| `errors.go` | Sentinel-ошибки `Err*`, `classify` (HTTP-статус → sentinel). |
| `options.go` | Функциональные опции `WithHTTPClient`/`WithPriceTable`/`WithBaseURL`. |
| `module.go` | fx `Module`, `NewConfig`, `newClient`. |
| `testkit/anthropictest/` | Тест-двойники: `Stub`, `ScriptStream`, `ErrStream`. |

## Поток данных

### Синхронный вызов (`Complete`)

```
Request (нативный)
   │  toParams: model←Config.DefaultModel при ""; max_tokens←16_000 при 0;
   │            blockToParam по каждому блоку; thinking adaptive|disabled;
   │            effort/JSONSchema → OutputConfig
   ▼
anthropic.MessageNewParams ──► sdk.Messages.New
   │                              │ ошибка → classify → Err* (record status=error)
   ▼                              ▼
anthropic.Message ── buildResponse ──► Response (Content, StopReason, Usage, Cost)
   │                                       │ costOf(model, Usage) по прайс-таблице
   ▼
record(status=ok, latency, Usage, Cost)
```

### Стриминговый вызов (`Stream`)

`Stream` возвращает итератор сразу; сам SSE-вызов ленив (открывается в первом
`Next`). Если маппинг запроса упал ещё до открытия SSE — возвращается `errStream`,
у которого `Next()=false`, а ошибка доступна через `Err()`/`Final()`.

```
Next() ──► s.Next() (SSE) ──► acc.Accumulate(ev) ──► translate(ev) → StreamEvent
                                   │ ошибка аккумуляции → err, finish()
   когда SSE исчерпан/ошибка ──► finish(): однократно record(...) по acc.Usage
Final() ──► buildResponse(&acc, model) → Response (авторитетные Usage+Cost)
```

Дефолт `max_tokens` для стрима — `64_000` (таймауты SSE не грозят, даём модели
простор), для sync — `16_000` (держим под HTTP-таймаутами SDK).

## Ключевые проектные решения (WHY)

- **`Config` vs `Option`.** `Config` — это env-контракт (грузится из ENV+Vault,
  имена тегов = public API). `Option` — программная настройка, которая в ENV не
  выражается: кастомный `*http.Client`, прайс-таблица с наценкой,
  override base URL для тестов. Поэтому base URL живёт и в `Config.BaseURL`, и в
  `WithBaseURL` — опция перекрывает Config.
- **Refusal — не ошибка.** Отказ safety-классификатора Claude приходит как
  успешный HTTP 200 со `StopReason=refusal`. Кит не превращает его в `error`:
  `Response.IsRefusal()` — явная проверка на стороне потребителя. Sentinel-ошибки
  `Err*` — только транспортные/протокольные сбои API.
- **Деньги целочисленно.** `USDMicros` (1 USD = 1_000_000) вместо float —
  суммирование без накопления ошибки. Ставки прайс-таблицы — per-MTok (за 1M
  токенов), web-search — за 1000 запросов.
- **Прайс-таблица — встроенная, но переопределяемая.** `defaultPrices` хранит
  канонные цены Anthropic. Наценка, или новая модель до апдейта кита — через
  `WithPriceTable` (передаётся целиком, не мерджится). Неизвестная модель → `Cost`
  возвращает 0/`known=false`; гейтвей сам решает fallback.
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
- **fx `Module` не provide'ит `Config`.** Симметрично с остальными китами
  (asymmetry note в `pgkit/module.go`): `Config` собирает приложение. Дешёвый
  путь — `fx.Provide(anthropickit.NewConfig)`. Опции (`WithPriceTable` и т.п.) —
  через свой конструктор поверх `New`.
