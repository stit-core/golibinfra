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
   tokens/cost и спан с `gen_ai.*`-атрибутами, каждый HTTP-запрос к API идёт
   через otelhttp-транспорт (спан попытки), сбои пишутся в `logkit.Logger`.

БД у кита нет: хранение запросов/ответов, леджер usage и любые агрегации —
ответственность сервиса.

## Компоненты

| Файл | Роль |
|---|---|
| `anthropickit.go` | `API`-интерфейс, `Client`, конструктор `New` (otelhttp-транспорт, retry-логирующий middleware SDK, таймаут/ретраи), `Stream`, `DefaultModel`, `Raw`, `costOf`/`priced`/`modelLabel`, `observe`/`logFailure` — единая точка завершения вызова. |
| `config.go` | `Config` — публичный env-контракт, `Validate` (включая `BaseURL`). |
| `messages.go` | Нативные типы (`Request`/`Response`/`Block`/`Message`/`ToolDef`), `Complete`, `CountTokens`, маппинг `toParams`/`blockToParam`/`cacheControl`/`toolParam`/`toToolChoice`, `buildResponse`. |
| `stream.go` | `Stream`-интерфейс, `sseStream` (поверх `ssestream.Stream`), `errStream`, нормализация событий (`translate`), однократная запись метрик в `finish` (исчерпание SSE или `Close`). |
| `usage.go` | `Usage`, `mapUsage` (из `anthropic.Usage`), `mapDeltaUsage` (из `message_delta`). |
| `pricing.go` | `USDMicros`, `Price`, `defaultPrices`, `tier`, `priceFor` (алиас + snapshot-id), `Price.cost`, свободная функция `Cost`. |
| `metrics.go` | `instruments` (4 otel-инструмента через `internal/otelkit`), `callResult`, `record`. |
| `spans.go` | Спан вызова (`anthropic.messages`/`anthropic.count_tokens`), `gen_ai.*`-атрибуты, `RecordError`. |
| `health.go` | Opt-in `Probe` — `healthkit.Probe` поверх `Models.Get(DefaultModel)`. |
| `errors.go` | Sentinel-ошибки `Err*`, `classify` (HTTP-статус → sentinel). |
| `options.go` | Функциональные опции `WithHTTPClient`/`WithPriceTable`/`WithBaseURL`. |
| `module.go` | fx `Module`, `NewConfig` (envconfig + `Validate`), `newClient`. |
| [`testkit/anthropictest/`](../../testkit/anthropictest/) | Тест-двойники: `Stub`, `ScriptStream`, `ScriptStreamErr`, `ErrStream`. |

## Поток данных

### Синхронный вызов (`Complete`)

```
Request (нативный)
   │  toParams: model←Config.DefaultModel при ""; max_tokens←16_000 при 0;
   │            blockToParam по каждому блоку; thinking adaptive|disabled+display;
   │            effort/JSONSchema → OutputConfig; AutoCache/UserID → cache_control/metadata
   │  max_tokens > 21_333 → ErrMaxTokensRequiresStreaming (без вызова и метрик)
   ▼
anthropic.MessageNewParams ──► sdk.Messages.New (спан anthropic.messages + otelhttp-спан попытки)
   │                              │ ошибка → observe(status=error, warn-лог) → classify → Err*
   ▼                              ▼
anthropic.Message ── buildResponse ──► Response (Content, StopReason, StopDetails, Usage, Cost, CostKnown)
   │                                       │ costOf(Response.Model, Usage) по прайс-таблице
   ▼
observe(status=ok, latency, Usage, Cost, priced=CostKnown)
```

### Стриминговый вызов (`Stream`)

`Stream` выполняет HTTP-запрос сразу (SDK открывает SSE в `NewStreaming`) и
возвращает итератор; `Next` читает события. Латентность меряется от момента
перед запросом. Если маппинг запроса упал ещё до HTTP-вызова — возвращается
`errStream` (`Next()=false`, ошибка через `Err()`/`Final()`, метрик нет —
вызова API не было).

```
Stream() ──► спан anthropic.messages ──► sdk.Messages.NewStreaming (HTTP-запрос уходит здесь)
Next() ──► s.Next() (SSE) ──► acc.Accumulate(ev) ──► translate(ev) → StreamEvent
                                   │ ошибка аккумуляции → err, finishLocked()
   когда SSE исчерпан/ошибка ──► finishLocked(): однократно observe(...) + span.End() по acc.Usage
Close() до конца SSE ──► s.Close() рвёт соединение; finishLocked(status=abandoned)
Usage() ──► mapUsage(acc.Usage) в любой момент, включая брошенный стрим
Final() ──► дрейн ок → Response; ошибка → (частичный Response, err); до дрейна → ErrStreamNotDrained
```

Состояние итератора под мьютексом: `Next`/`Event`/`Err`/`Final` вызываются из
одной горутины, `Close` и `Usage` — из любой. Чтение SSE идёт вне мьютекса,
иначе `Close` ждал бы следующего события вместо разрыва соединения; учёт
брошенного стрима пишет та горутина, которая проснулась на разорванном чтении,
и ровно один раз (`done`).

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
  (`option.WithRequestTimeout`), который SDK ретраит, так что wall-clock вызова
  доходит до `Timeout × (MaxRetries+1)`; для стрима дедлайн попытки живёт до
  конца чтения SSE, поэтому под длинные генерации его задают с запасом; ctx
  вызова ограничивает всё вместе. Повторные попытки SDK видны в логе на `Debug`
  через middleware по заголовку `X-Stainless-Retry-Count`.
- **Config — единственный источник credentials и endpoint.** SDK-клиент
  создаётся с `option.WithoutEnvironmentDefaults()`: иначе SDK сам подхватывает
  ANTHROPIC_AUTH_TOKEN, ANTHROPIC_BASE_URL, ANTHROPIC_PROFILE и профили с диска,
  и протухший токен в окружении пода даёт 401 при валидном ключе в конфиге.
- **Потолок sync `max_tokens`.** Non-streaming-ответ API отдаёт темпом ~128K
  токенов в час, то есть больше 21_333 токенов в 10-минутный бюджет не
  укладывается. Кит отвергает такой `Complete` типизированной
  `ErrMaxTokensRequiresStreaming` до HTTP-вызова, вместо нетипизированной ошибки
  SDK и метрики несуществующего вызова.
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
- **Усечённый `buildResponse`.** В `Response.Content` маппятся text, tool_use,
  thinking и redacted_thinking. Первые три — рабочий контент, redacted_thinking —
  обязательное условие реплея: ход с `tool_use` API принимает только вместе с
  неизменёнными thinking-блоками этого хода. Остальные блоки SDK (server-tool use
  и т.п.) в нативный ответ не попадают — для них есть `Raw()`.
- **Кардинальность label `model` ограничивает кит.** Модель приходит от
  потребителя (в гейтвее — от его клиента), поэтому в метрику она попадает
  только если есть в прайс-таблице; всё прочее — `model="other"` плюс один
  warn-лог с исходной строкой. Инвариант «потребитель шлёт только модели из
  своего allowlist» так не нужен, а новая модель видна по `priced="false"`.
- **`Raw()` как escape hatch.** Managed Agents (`Raw().Beta.Agents`/`Sessions`),
  batches, files и прочее, что кит не покрывает нативно, доступно напрямую через
  SDK-клиент. Это сознательный trade-off: кит покрывает Messages-поверхность, не
  пытаясь обернуть весь SDK.
- **Авторитетный Usage стрима — из `Final`.** В событии `message_delta` usage
  кумулятивен, но cache-write split по TTL там недоступен (только лумпованный
  total). Точный финальный `Usage` (с разбивкой 5m/1h) берётся из
  аккумулированного `Message.Usage` в `Stream.Final`.
- **Прерванный стрим тоже учитывается.** `Close` до исчерпания SSE (`abandoned`,
  в том числе из другой горутины) и обрыв SSE или отмена ctx (`error`) пишут
  метрики с накопленным usage/cost, а
  `Final` отдаёт вместе с ошибкой частичный `Response`; `Stream.Usage()` доступен
  в любой момент. Иначе прерванные генерации выпадали бы из учёта, хотя API их
  тарифицирует.
- **Проба — opt-in.** `Probe` (`Models.Get(DefaultModel)`) кит не регистрирует
  сам: readiness сервиса, привязанный к стороннему платному API, — решение
  деплоя, а не библиотеки.
- **fx `Module` не provide'ит `Config`.** Симметрично с остальными китами
  (asymmetry note в `pgkit/module.go`): `Config` собирает приложение. Дешёвый
  путь — `fx.Provide(anthropickit.NewConfig)`. Опции (`WithPriceTable` и т.п.) —
  через свой конструктор поверх `New`.
