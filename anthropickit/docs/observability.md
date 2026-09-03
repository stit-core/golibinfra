# Observability anthropickit

Единый источник правды по телеметрии кита. Имена и labels — точно как в коде
(`metrics.go`, `anthropickit.go`).

## Метрики

Meter: `git.stit.tech/stit-core/golibinfra/anthropickit` (через
`internal/otelkit`). Инструменты создаются в `newInstruments()` при `New`;
ошибка создания инструмента — `panic`.

Запись метрик — в `Client.observe` → `instruments.record(ctx, callResult)`,
вызывается из `Complete`, `CountTokens` и `sseStream.finish()` (однократно:
по исчерпании SSE либо при `Close` до конца стрима).

| Метрика | Тип | Labels | Когда / что |
|---|---|---|---|
| `anthropic_api_calls_total` | counter (`Int64Counter`) | `model`, `op`, `status`, `priced` | +1 на каждый HTTP-вызов API. |
| `anthropic_api_latency_seconds` | histogram (`Float64Histogram`, unit `s`) | `model`, `op` | Длительность вызова от момента перед запросом до завершения (sync — ответ SDK, stream — исчерпание SSE или `Close`). Пишется при любом `status`. |
| `anthropic_api_tokens_total` | counter (`Int64Counter`) | `model`, `kind` | Токены из `Usage` при `status` ∈ `ok`/`abandoned`. По одному добавлению на ненулевой `kind`. `op=count_tokens` токены не пишет — подсчёт не тарифицируется. |
| `anthropic_api_cost_micros_total` | counter (`Int64Counter`) | `model` | Стоимость в микро-долларах (`USDMicros`) при `status` ∈ `ok`/`abandoned` и `cost > 0`; для модели вне прайс-таблицы серия не появляется. |

### Значения labels

| Label | Значения |
|---|---|
| `op` | `complete` (`Complete`), `stream` (`Stream`), `count_tokens` (`CountTokens`). |
| `status` | `ok` — успешный ответ / исчерпанный стрим; `error` — ошибка HTTP/транспорта/ctx (usage и cost не учитываются); `abandoned` — `Close` до конца SSE (usage/cost — накопленные к этому моменту). |
| `priced` | `true`/`false` — есть ли запрошенная модель в прайс-таблице клиента (`priceFor`, с учётом snapshot-id). Ставится и на `error`-вызовах. |
| `kind` | `input` (`InputTokens`), `output` (`OutputTokens`), `cache_write` (`CacheWrite5mTokens + CacheWrite1hTokens`), `cache_read` (`CacheReadTokens`), `thinking` (`ThinkingTokens`). |

Нулевые значения токенов не эмитятся (серия для такого `kind` не появляется).
`WebSearchRequests`/`WebFetchRequests` в `anthropic_api_tokens_total` **не**
попадают — они учитываются только в стоимости (`Cost`).

### Семантика label `model`

`model` берётся из `Request.Model`, а при пустом — из `Config.DefaultModel`
(резолвится в `toParams`). Это запрошенная модель, не обязательно тот идентификатор,
что вернул API. (В `Response.Model` лежит `Message.Model` из ответа SDK — по нему
считается `Cost`; в метрики идёт запрошенное значение.)

Ошибка маппинга запроса до HTTP-вызова (`errStream`, `ErrInvalidRequest` из
`toParams`) метрик не пишет — вызова API не было.

## Спаны

Кит **не открывает собственных спанов**, но каждый HTTP-вызов к API идёт через
`otelhttp.NewTransport` — SDK получает `*http.Client` кита. На каждый запрос
(включая повторы SDK) — клиентский спан `SpanKind=CLIENT` с атрибутами otelhttp
(`http.request.method`, `url.full`, `server.address`, `http.response.status_code`),
дочерний к спану вызывающего из `ctx`; trace-context уходит в заголовках запроса.
Tracer читается из глобального `otel.GetTracerProvider()`.

## Логи

`New(cfg, log, ...)` принимает `logkit.Logger` (`nil` → `logkit.Nop()`). Кит пишет:

| Уровень | Сообщение | Когда | Поля |
|---|---|---|---|
| `Warn` | `anthropickit: api error` | API вернул ошибку (любой `Err*` c `*anthropic.Error`) в `Complete`/`Stream`/`CountTokens` | `op`, `model`, `status` (HTTP), `type` (тип ошибки API), `request_id`, `retry_after` (если сервер прислал) |
| `Warn` | `anthropickit: request failed` | транспортная ошибка / таймаут / ошибка SSE-парсинга | `op`, `model`, `error` |
| `Debug` | `anthropickit: request canceled` | `ctx` вызова отменён | `op`, `model` |
| `Debug` | `anthropickit: retrying request` | SDK выполняет повторную попытку (заголовок `X-Stainless-Retry-Count` > 0) | `attempt`, `method`, `path` |

## Resource attributes

`anthropickit` **не** выставляет resource attrs (`service.name`,
`service.version`, `deployment.environment.name`) — их несёт OTel-resource,
настроенный потребителем в его проводке OTel SDK. Кит лишь читает meter/tracer
из глобальных otel-провайдеров; без зарегистрированного `MeterProvider`/
`TracerProvider` — no-op.
