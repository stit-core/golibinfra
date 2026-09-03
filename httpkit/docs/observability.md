# httpkit — observability

Сигналы эмитятся через глобальные OTel-провайдеры; без `MeterProvider` /
`TracerProvider` инструменты — no-op. Resource attrs и экспорт настраивает
потребитель (`telemetry` или своя проводка).

## Метрики

Instrumentation-scope `git.stit.tech/stit-core/golibinfra/httpkit`. Гистограмма
длительности — явные bucket'ы (сек): `0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
1, 2.5, 5, 10, 30`.

| Метрика | Тип | Labels | Эмитится | Назначение |
|---|---|---|---|---|
| `http_requests_total` | Int64 Counter | `method`, `path`, `status` | `Metrics` | счётчик запросов |
| `http_request_duration_seconds` | Float64 Histogram | `method`, `path`, `status` | `Metrics` | длительность обработки |
| `http_panics_total` | Int64 Counter | `path` | `Recovery` | восстановленные panic |
| `http_max_body_rejections_total` | Int64 Counter | `path` | `MaxBody` | ответы 413 |
| `http_rate_limited_total` | Int64 Counter | — | `RateLimit` | ответы 429 |
| `http_idempotency_total` | Int64 Counter | `result` | `Idempotency` | исходы: `stored`, `replayed`, `conflict`, `in_progress`, `skipped`, `error` |

Label `path` везде — chi route-pattern (`/users/{id}`); запрос без маршрута
получает `unmatched`, так что сканы 404 не растят кардинальность.

## Спаны

`OTel` стоит вторым в цепочке (после `RequestID`): `otelhttp` открывает
server-span и извлекает входящий `traceparent`, а все последующие middleware,
включая `AccessLog` и `Recovery`, работают внутри спана. `Metrics` после
роутинга даёт спану имя `METHOD /route/{param}`. Собственные RED-метрики
`otelhttp` выключены, чтобы не дублировать `http_requests_total` другим словарём.

## Логи

`AccessLog` пишет строку на запрос: `method`, `path`, `route`, `status`, `bytes`,
`dur`; уровень по статусу (`≥500` Error, `≥400` Warn, иначе Info). `request_id`
и `trace_id`/`span_id` добавляет `logkit` из ctx.

`Recovery` логирует `panic recovered` со `stack`; `/ready` логирует каждую
упавшую пробу (`readiness probe failed`, `probe`, `err`) на `Warn`, не отдавая
текст ошибки клиенту.
