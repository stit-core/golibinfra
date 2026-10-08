# httpkit — observability

Сигналы эмитятся через глобальные OTel-провайдеры; без `MeterProvider` /
`TracerProvider` инструменты — no-op. Resource attrs и экспорт настраивает
потребитель (`telemetry` или своя проводка).

Таблицы этой страницы — контракт: их читают тесты `httpkit/signals_test.go` и сверяют с
сигналами живого `Module` (метрики — имя, тип, unit, labels и их значения; строки лога —
сообщение, уровень, поля; спаны) и со строками лога в коде кита. Расхождение доки с кодом в
любую сторону — красный `make check`.

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
| `http_idempotency_total` | Int64 Counter | `result` | `Idempotency` | исходы идемпотентности |

Значения labels — конечные множества:

| Label | Значения |
|---|---|
| `method` | `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS`, `CONNECT`, `TRACE`; любой другой токен клиента — `other` |
| `path` | chi route-pattern (`/users/{id}`); путь без маршрута — `unmatched` |
| `status` | код ответа |
| `result` | `stored`, `replayed`, `conflict`, `in_progress`, `skipped`, `error` |

Label `path` — паттерн маршрута, а не путь запроса: сканы 404 не растят кардинальность.
Ответ, который выдан до роутера или вместо хендлера, — 429 лимитера, preflight 204, 405 —
считается под маршрутом своего пути: паттерн ищется по пути запроса (сначала с его
методом, затем с остальными). `unmatched` — только у пути, для которого маршрута нет ни
с одним методом. В подроутере (`Router().Mount`) 404 и 405 идут под паттерном
монтирования — `/v1/*`: его роутер выставил до подроутера.

Panic-запросы, 429 и preflight 204 попадают в `http_requests_total` наравне с
остальными: `Metrics` стоит выше `Recovery`, `CORS` и `RateLimit` и пишет сэмпл
через `defer`.

## Спаны

`OTel` стоит третьим в цепочке (после `RequestID` и `ClientIP`): `otelhttp` открывает
server-span и извлекает входящий `traceparent`, а все последующие middleware,
включая `AccessLog` и `Recovery`, работают внутри спана. Имя спана —
`METHOD /route/{param}`: его ставит `Metrics` по паттерну маршрута — тому же, что в label
`path`, — в том числе когда middleware ниже `OTel` заменил объект запроса
(`r.WithContext`). `Metrics` добавляет на спан `http.route`, `request_id` и
`client.address` — IP из `ClientIP` вместо `X-Forwarded-For`, который прислал клиент. У
запроса на путь без маршрута имя спана — `METHOD`, атрибута `http.route` нет. Собственные
RED-метрики `otelhttp` выключены, чтобы не дублировать `http_requests_total` другим словарём.

Пути из `HTTP_QUIET_PATHS` (по умолчанию `/health`, `/ready`) спан не открывают:
пробы оркестратора идут каждые несколько секунд и трейсы им не нужны.

## Логи

`request_id` и `trace_id`/`span_id` в каждую строку добавляет `logkit` из ctx. Поля после
«необязательные» есть не в каждой записи.

| Сообщение | Уровень | Поля | Источник |
|---|---|---|---|
| `http` | Error, Warn, Info, Debug | `method`, `path`, `route`, `status`, `bytes`, `duration_ms` | `AccessLog`: строка на запрос |
| `panic recovered` | Error | `panic`, `stack` | `Recovery` |
| `handler error` | Error | `status`, `code`; необязательные: `operation`, `error` | любой 5xx из huma (Transformer сервера) |
| `auth rejected` | Debug | `error`; необязательные: `code` | `HumaAuth`: 401 или 4xx `APIError` extractor'а |
| `auth backend unavailable` | Warn | `error` | `HumaAuth`: 503, extractor вернул `ErrAuthUnavailable` |
| `auth failed` | Error | `error`, `code` | `HumaAuth`: extractor вернул 5xx `APIError` |
| `auth not configured` | Error | `operation` | `HumaAuth`: у схемы операции нет extractor'а |
| `idempotency store failed` | Warn | `op`, `error`, `fail_closed` | `Idempotency`: отказ store |
| `idempotency reservation expired before the response` | Warn | `reserve_ttl` | `Idempotency`: handler шёл дольше резерва |
| `request deadline exceeded` | Warn | `timeout` | дедлайн запроса |
| `readiness probe failed` | Warn | `probe`, `error` | `/ready`: проба вернула ошибку |
| `readiness probe panicked` | Error | `probe`, `panic`, `stack` | `/ready`: panic пробы |
| `http server listening` | Info | `addr` | старт |
| `http server stopped` | Error | `error` | падение `Serve` |
| `http server stop deadline: requests cancelled, connections closed` | Warn | `requests`, `error` | дедлайн остановки |
| `http server error` | Warn | `error` | ошибки net/http из `http.Server.ErrorLog` |
| `x-forwarded-for ignored: HTTP_TRUST_PROXY_HOPS=0, client ip is the peer address` | Warn | `peer` | `ClientIP`, один раз |
| `healthcheck failed` | Error | `error` | подкоманда `Healthcheck`: `/health` не ответил 200 или конфиг не прочитан |

- `http` — уровень по статусу: `≥500` Error, `≥400` Warn, иначе Info; успешные ответы на
  `HTTP_QUIET_PATHS` уходят на Debug, а их ошибки остаются на Warn/Error. `route` — паттерн
  маршрута, как label `path`; `duration_ms` — миллисекунды, float.
- `handler error` — клиенту уходит только статус, код и `request_id`; причина — в `error`.
- `idempotency store failed` — `op`: `get`, `reserve`, `set`, `release`; тот же отказ считает
  `http_idempotency_total{result="error"}`.
- `idempotency reservation expired before the response` — handler шёл дольше
  `HTTP_IDEMPOTENCY_RESERVE_TTL`, ответ не сохранён (`http_idempotency_total{result="skipped"}`).
- `request deadline exceeded` — ответ не начат, клиенту ушёл `503 REQUEST_TIMEOUT`
  ([configuration.md § Дедлайн запроса](configuration.md#дедлайн-запроса)).
- `readiness probe failed` — текст ошибки клиенту не отдаётся; при panic проба `down`,
  процесс жив.
- `http server listening` — `addr` фактический; после `http server stopped` приложение гасится.
- `http server stop deadline: requests cancelled, connections closed` — `requests` — число
  запросов в полёте: их ctx отменён, соединения закрыты
  ([configuration.md § Остановка](configuration.md#остановка)).
- `x-forwarded-for ignored: …` — пришёл `X-Forwarded-For`, а доверенных прокси 0.
