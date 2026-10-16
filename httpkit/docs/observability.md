# httpkit — observability

Сигналы эмитятся через глобальные OTel-провайдеры; без `MeterProvider` /
`TracerProvider` инструменты — no-op. Resource attrs и экспорт настраивает
потребитель (`telemetry` или своя проводка).

Таблицы этой страницы — контракт: их читают тесты `httpkit/signals_test.go`. Сценарий на
живом `Module` сверяет с ними метрики (имя, тип, unit, labels и их значения), строки лога
(сообщение, уровень, поля) и спаны (имя, kind, атрибуты кита). Статическая сверка по коду кита
держит то, чего сценарий не вызывает: имена инструментов метрик, ключи labels и атрибутов
спана, строки лога — сообщение, уровень и поля, названные литералом. Расхождение таблицы с
кодом в любую сторону — красный `make check`. Вне сверки — проза страницы.

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
(`r.WithContext`); у запроса на путь без маршрута имя — `METHOD`. К атрибутам `otelhttp`
`Metrics` добавляет свои:

| Атрибут спана | Значение |
|---|---|
| `http.route` | паттерн маршрута, как label `path`; у пути без маршрута атрибута нет |
| `request_id` | идентификатор запроса из `RequestID` |
| `client.address` | IP из `ClientIP` — вместо `X-Forwarded-For`, который прислал клиент |

Trace-context клиента (`traceparent`, `tracestate`, `baggage`) по состояниям:

| `HTTP_TRUST_TRACE_CONTEXT` | SDK-провайдер трасс | В ctx хендлера, в логе и в исходящих вызовах |
|---|---|---|
| `false` | есть | новый корень сервера со ссылкой на контекст клиента; `tracestate` и `baggage` клиента не читаются |
| `false` | нет (`OTEL_SDK_DISABLED=true`, `telemetry` в графе нет) | ничего: `trace_id` в логе нет, заголовков вниз нет |
| `true` | есть | спан — ребёнок контекста клиента: его `trace_id`, решение о сэмплировании, `tracestate` и `baggage` |
| `true` | нет | контекст клиента как пришёл: тот же `traceparent`, `tracestate` и `baggage` |

Собственные RED-метрики `otelhttp` выключены, чтобы не дублировать `http_requests_total`
другим словарём.

Пути из `HTTP_QUIET_PATHS` (по умолчанию `/health`, `/ready`) спан не открывают:
пробы оркестратора идут каждые несколько секунд и трейсы им не нужны.

## Логи

`request_id` в каждую строку добавляет `logkit` из ctx, `trace_id`/`span_id` — когда в ctx есть
спан (§ Спаны: без SDK-провайдера трасс — только у запроса с контекстом клиента, которому сервис
доверяет). Поля после «необязательные» есть не в каждой записи.

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
| `huma group operation body limit is capped by its literal: MaxBodyBytes: -1 in the literal lifts it` | Warn | `operation`, `limit`, `requested` | старт: операция группы huma, тело которой huma режет раньше лимита кита или хука |
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
  `http_idempotency_total{result="error"}`. `op=reserve` с текстом `key budget of the client is
  exhausted` — клиент исчерпал бюджет ключей (store исправен).
- `idempotency reservation expired before the response` — handler шёл дольше
  `HTTP_IDEMPOTENCY_RESERVE_TTL`, ответ не сохранён (`http_idempotency_total{result="skipped"}`).
- `request deadline exceeded` — ответ не начат, клиенту ушёл `503 REQUEST_TIMEOUT`
  ([configuration.md § Дедлайн запроса](configuration.md#дедлайн-запроса)).
- `readiness probe failed` — текст ошибки клиенту не отдаётся; при panic проба `down`,
  процесс жив.
- `huma group operation body limit is capped by …` — тело операции группы huma с префиксом huma читает по
  `MaxBodyBytes` её литерала (у `Body` без него — 1 MiB): `limit` — этот лимит, им тело и режется, `requested`
  — лимит кита или хука (не больше нуля — без предела); `MaxBodyBytes: -1` в литерале возвращает операции
  `requested` ([configuration.md § MaxBodyConfig](configuration.md#maxbodyconfig)).
- `http server listening` — `addr` фактический; после `http server stopped` приложение гасится.
- `http server stop deadline: requests cancelled, connections closed` — `requests` — число
  запросов в полёте: их ctx отменён, соединения закрыты
  ([configuration.md § Остановка](configuration.md#остановка)).
- `x-forwarded-for ignored: …` — пришёл `X-Forwarded-For`, а доверенных прокси 0.
