# httpkit — observability

Сигналы эмитятся через глобальные OTel-провайдеры; без `MeterProvider` /
`TracerProvider` инструменты — no-op. Resource attrs и экспорт настраивает
потребитель (`telemetry` или своя проводка).

## Метрики

Instrumentation-scope `git.stit.tech/stit-core/golibinfra/httpkit`. Гистограмма
длительности — `WithUnit("s")` и общие для всех китов bucket'ы
`otelkit.DurationBuckets()`. Инструменты создаются один раз на процесс и
переиспользуются всеми middleware.

| Метрика | Тип | Labels | Эмитится | Назначение |
|---|---|---|---|---|
| `http_requests_total` | Int64 Counter | `method`, `path`, `status` | `Metrics` | счётчик запросов |
| `http_request_duration_seconds` | Float64 Histogram | `method`, `path`, `status` | `Metrics` | длительность обработки |
| `http_panics_total` | Int64 Counter | `path` | `Recovery` | восстановленные panic |
| `http_max_body_rejections_total` | Int64 Counter | `path` | `MaxBody` | ответы 413 |
| `http_rate_limited_total` | Int64 Counter | — | `RateLimit` | ответы 429 |
| `http_idempotency_total` | Int64 Counter | `result` | `Idempotency` | исходы: `stored`, `replayed`, `conflict`, `in_progress`, `skipped`, `error` |

Label `path` везде — chi route-pattern (`/users/{id}`); запрос без маршрута
получает `unmatched`, так что сканы 404 не растят кардинальность. Label `method`
— только стандартные методы (`GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`,
`OPTIONS`, `CONNECT`, `TRACE`), любой другой токен клиента становится `other`.

Panic-запросы, 429 и preflight 204 попадают в `http_requests_total` наравне с
остальными: `Metrics` стоит выше `Recovery`, `CORS` и `RateLimit` и пишет сэмпл
через `defer`. У 429 и preflight `path` — `unmatched`: их отвергли до роутинга,
и chi-паттерн ещё не известен.

## Спаны

`OTel` стоит третьим в цепочке (после `RequestID` и `ClientIP`): `otelhttp` открывает
server-span и извлекает входящий `traceparent`, а все последующие middleware,
включая `AccessLog` и `Recovery`, работают внутри спана. Имя спана —
`METHOD /route/{param}`: его ставит `Metrics` по chi-паттерну после роутинга, в том
числе когда middleware ниже `OTel` (свой или `Idempotency` с extractor'ом) заменил
объект запроса; без маршрута — `METHOD`. `Metrics` добавляет на спан `http.route`,
`request_id` и `client.address` — IP из `ClientIP` вместо `X-Forwarded-For`, который
прислал клиент. Собственные RED-метрики `otelhttp` выключены, чтобы не дублировать
`http_requests_total` другим словарём.

Пути из `HTTP_QUIET_PATHS` (по умолчанию `/health`, `/ready`) спан не открывают:
пробы оркестратора идут каждые несколько секунд и трейсы им не нужны.

## Логи

`AccessLog` пишет строку на запрос: `method`, `path`, `route`, `status`,
`bytes`, `duration_ms` (миллисекунды, float); уровень по статусу (`≥500` Error,
`≥400` Warn, иначе Info). Успешные ответы на `HTTP_QUIET_PATHS` уходят на Debug,
а их ошибки остаются на Warn/Error. `request_id` и `trace_id`/`span_id`
добавляет `logkit` из ctx.

Остальные строки кита:

- `panic recovered` (Error) со `stack` — `Recovery`.
- `handler error` (Error) с `operation`, `status`, `code` и причиной `err` — любой
  5xx из huma (Transformer сервера); клиенту уходит только статус, код и `request_id`.
- `auth rejected` (Debug) / `auth backend unavailable` (Warn) / `auth failed` (Error, с
  `code`: extractor вернул 5xx `APIError`) / `auth not configured` (Error, с `operation`) —
  `HumaAuth`.
- `idempotency store failed` (Warn) с `op` (`get`, `reserve`, `set`, `release`), `err` и
  `fail_closed` — `Idempotency`; тот же отказ считает `http_idempotency_total{result="error"}`.
- `readiness probe failed` (Warn) с `probe` и `err` — `/ready`; текст ошибки
  клиенту не отдаётся.
- `http server listening` (Info) с фактическим `addr` — старт;
  `http server stopped` (Error) — падение `Serve`, после которого приложение
  гасится.
- `http server error` (Warn) — ошибки net/http из `http.Server.ErrorLog`.
- `x-forwarded-for ignored: HTTP_TRUST_PROXY_HOPS=0, client ip is the peer address` (Warn,
  один раз) с `peer` — `ClientIP`: пришёл `X-Forwarded-For`, а доверенных прокси 0.
