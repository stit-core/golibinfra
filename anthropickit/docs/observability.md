# Observability anthropickit

Единый источник правды по телеметрии кита. Имена и labels — точно как в коде
(`metrics.go`).

## Метрики

Meter: `git.stit.tech/stit-core/golibinfra/anthropickit`. Инструменты создаются в
`newInstruments()` при `New`; ошибка создания инструмента — `panic`.

Запись метрик — в `instruments.record(ctx, model, status, dur, usage, cost)`,
вызывается из `Complete` (после вызова) и из `sseStream.finish()` (однократно по
завершении дрейна стрима).

| Метрика | Тип | Labels | Когда / что |
|---|---|---|---|
| `anthropic_api_calls_total` | counter (`Int64Counter`) | `model`, `status` | +1 на каждый вызов. `status` ∈ `ok` / `error`. |
| `anthropic_api_latency_seconds` | histogram (`Float64Histogram`, unit `s`) | `model` | Длительность вызова (sync — вокруг `Messages.New`; stream — от открытия до `finish`). Пишется и при `error`. |
| `anthropic_api_tokens_total` | counter (`Int64Counter`) | `model`, `kind` | Только при `status=ok`. По одному добавлению на ненулевой `kind`. |
| `anthropic_api_cost_micros_total` | counter (`Int64Counter`) | `model` | Только при `status=ok` и `cost > 0`. Стоимость в микро-долларах (`USDMicros`). |

### Значения label `kind` у `anthropic_api_tokens_total`

| `kind` | Источник из `Usage` |
|---|---|
| `input` | `InputTokens` |
| `output` | `OutputTokens` |
| `cache_write` | `CacheWrite5mTokens + CacheWrite1hTokens` (суммарно) |
| `cache_read` | `CacheReadTokens` |
| `thinking` | `ThinkingTokens` |

Нулевые значения не эмитятся (серия для такого `kind` не появляется).
`WebSearchRequests`/`WebFetchRequests` в `anthropic_api_tokens_total` **не**
попадают — они учитываются только в стоимости (`Cost`).

### Семантика label `model`

`model` берётся из `Request.Model`, а при пустом — из `Config.DefaultModel`
(резолвится в `toParams`). Это запрошенная модель, не обязательно тот идентификатор,
что вернул API. (В `Response.Model` лежит `Message.Model` из ответа SDK, но в
метрики идёт запрошенное значение.)

## Спаны

Кит **не открывает собственные спаны.** HTTP-спан вызова к Anthropic API ведёт
сам `anthropic-sdk-go` через свой otel-инструментированный транспорт. Контекст
(`ctx`) пробрасывается в SDK как есть — trace propagation работает, если в `ctx`
есть активный спан вызывающего.

## Resource attributes

`anthropickit` **не** выставляет resource attrs (`service.name`,
`service.version`, `deployment.environment.name`, `tenant`,
`service.namespace`). Их ставит приложение через `telemetry.Config`
(`service.*`/`environment`) и корп `otel-collector` (`tenant`/
`service.namespace`). Кит лишь читает meter из глобального otel-провайдера
(`otel.Meter`).
