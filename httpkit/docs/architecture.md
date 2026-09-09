# httpkit — архитектура

## Два слоя: chi + huma

`Server` несёт оба слоя:

- **`*chi.Mux`** (`Router()`) — транспорт: маршрутизация, точка крепления
  net/http-middleware (`router.Use`).
- **`huma.API`** (`API()`/`BindAPI()`) — типобезопасные операции с
  авто-генерацией OpenAPI и валидацией ввода/вывода. Поверх chi через
  `humachi.New`.

Бизнес-роуты регистрируются на `huma.API` (`huma.Get`/`huma.Register`),
инфраструктурные middleware — на `*chi.Mux`. Auth-middleware живёт на уровне
huma (`api.UseMiddleware`), потому что ему нужен доступ к `Operation().Security`.

`*http.Server` собирается в `NewServer` из `Config` (`Addr`, `ReadTimeout`,
`ReadHeaderTimeout`, `WriteTimeout`, `IdleTimeout`); handler — chi router.
`ErrorLog` сервера направлен в `logkit` (опция `WithLogger`), поэтому ошибки
net/http (`TLS handshake error`, `Accept error`) идут в тот же sink, что и
остальные логи сервиса.

## Старт и остановка

`Listen()` занимает порт синхронно и идемпотентно; `Serve()` обслуживает
соединения до `Stop(ctx)` (graceful `Shutdown`). `Module` вызывает `Listen` в
`OnStart` — занятый порт или невалидный адрес валят старт приложения, а не
превращаются в строку лога при уже «запущенном» fx-графе; фактический адрес
пишется в лог (`http server listening`, важно при `HTTP_ADDR=:0`). Ошибка
`Serve` после старта гасит приложение через `fx.Shutdowner`: процесс с мёртвым
listener'ом не должен выглядеть живым. `Addr()` отдаёт фактический адрес.

## Ленивый BindAPI — порядок имеет значение

`huma.API` создаётся не в `NewServer`, а лениво в `BindAPI()`: `humachi.New`
сразу регистрирует `/openapi.json` и `/docs`, после чего chi запрещает
`router.Use(...)`. Контракт:

1. `NewServer(cfg, opts...)` — router + `*http.Server`; middleware из
   `WithMiddleware` регистрируются здесь.
2. `router.Use(middleware...)` — остальная net/http-цепочка (при ручной сборке).
3. `BindAPI()` — создаёт `huma.API`.
4. `huma.Register` / `huma.Get`; `api.UseMiddleware(...)` для huma-уровня.

`BindAPI` идемпотентен, `API()` — алиас. `Module` соблюдает порядок сам.

### Точки расширения

- `WithSecuritySchemes(entries...)` / fx-группа `httpkit.security_scheme` —
  `components.securitySchemes`; дефолтных схем httpkit не выставляет.
- `WithMiddleware(mw...)` / fx-группа `httpkit.middleware` — chi-middleware
  потребителя; в `Module` они встают **внутрь** цепочки кита, поэтому их panic
  ловит `Recovery`, а их ответы попадают в метрики и access-log.
- `WithHumaConfig(fn)` / fx-группа `httpkit.huma_config` — правка `huma.Config`
  до создания API (`Servers`, `DocsRenderer`, `Transformers`, свои
  `OnAddOperation`). Хуки потребителя применяются после кита.
- `HTTP_DOCS_ENABLED=false` — сервис без `/docs` и `/openapi.*`.

## Конвейер middleware и порядок

chi применяет middleware в порядке регистрации: первый в списке — внешний,
запрос идёт сверху вниз, ответ — снизу вверх.

| # | Middleware | Условие | Зачем именно здесь |
|---|---|---|---|
| 1 | `RequestID` | всегда | `X-Request-ID` в ctx до всего остального: он попадает в каждую строку лога и в атрибут спана |
| 2 | `OTel` | всегда | server-span и входящий `traceparent`; всё ниже работает внутри спана, поэтому `trace_id` есть в access-log и panic-log |
| 3 | `AccessLog` | всегда | строка на запрос с `route`, `status`, `bytes`, `duration_ms` |
| 4 | `Metrics` | всегда | RED на chi route-pattern; ниже него — всё, что может ответить клиенту |
| 5 | `Recovery` | всегда | ловит panic из всего, что ниже: 500 + `http_panics_total`; `http.ErrAbortHandler` пробрасывает |
| 6 | `SecurityHeaders` | всегда | заголовки на любой ответ, включая 429 и 413 |
| 7 | `CORS` | если `CORS.Origins` не пуст | preflight отвечает 204 до лимитера и тела |
| 8 | `RateLimit` | если `RateLimit.RPS > 0` | per-IP bucket; дешёвый отказ до тяжёлой работы |
| 9 | `MaxBody` | если `MaxBody.MaxBytes > 0` | `MaxBytesReader` до чтения body handler'ом |
| 10 | `Idempotency` | если в DI есть `IdempotencyStore` | дедуп write-запросов по `Idempotency-Key` |
| 11 | middleware потребителя | группа `httpkit.middleware` | ближе всего к handler'у |

`HumaAuth` — отдельный слой на `huma.API`.

Почему так: `RequestID` и `OTel` первыми, чтобы контекст запроса был полным у
всех остальных. `AccessLog` и `Metrics` — **над** `Recovery`, `CORS` и
`RateLimit`: иначе panic-запросы (самые тяжёлые 500-е), 429 и preflight
отвечали бы выше них и не попадали ни в `http_requests_total`, ни в access-log.
Обе записи сделаны через `defer`, поэтому запрос учитывается и когда снизу
прилетела panic. Дальше — от дешёвого отказа к дорогой работе: заголовки,
preflight, лимитер, лимит тела, идемпотентность.

### Зависимости через fx

- `logkit.Logger` — обязателен (`Recovery`, `AccessLog`, `HumaAuth`, `/ready`,
  error-hook, `ErrorLog`).
- `IdempotencyStore` (`optional`) — есть в графе → `Idempotency` с
  `Config.Idempotency` (`HTTP_IDEMPOTENCY_*`).
- `AuthExtractor` (`optional`) → `HumaAuth` и scope идемпотентности; нет →
  auth no-op.
- `[]SecuritySchemeEntry` (группа `httpkit.security_scheme`) → OpenAPI.
- `[]func(http.Handler) http.Handler` (группа `httpkit.middleware`) → цепочка.
- `[]func(*huma.Config)` (группа `httpkit.huma_config`) → huma.
- `[]healthkit.Probe` (группа `healthkit.probes`) → `/ready`. Тип именованный:
  `httpkit.Probe` — alias `healthkit.Probe`, а `pgkit.Probe`, `natskit.Probe`,
  `cachekit.Probe` возвращают его же. Регистрация — `healthkit.Provide(ctor)`.

## Wrapper ответа

Все middleware оборачивают `http.ResponseWriter` одним типом: он считает статус
и байты, повторный `WriteHeader` игнорирует, реализует `Unwrap()` для
`http.ResponseController`, `Flush()` для SSE/chunked и `Hijack()` для
WebSocket-upgrade. Повторное оборачивание того же writer'а не создаёт новый слой.

## RequestID

Принимает `X-Request-ID` клиента только печатным ASCII без пробелов длиной до
128 байт (id попадает в каждую строку лога), иначе генерирует UUIDv7
(`idkit.NewV7`). Ключ ctx — `internal/requestid`, общий с `httpclient`
(прокидывает заголовок дальше) и `grpckit` (интерсептор пишет тот же ключ).
`init()` регистрирует `logkit.RegisterAttr`: любой ctx-scoped лог-вызов получает
`request_id` автоматически.

## Error-модель

`APIError` — problem-объект `{type, title, status, detail, instance, code}`
(`omitempty`), реализует `error`, `huma.StatusError` и `huma.ContentTypeFilter`,
поэтому из любого слоя уходит с `Content-Type: application/problem+json`.

- `Error(code, status, detail)`, хелперы `BadRequest`…`Internal`, `Unavailable`.
- `MapErr(err, mappings...)` — таблица `ErrorMapping{Match, Code, Status, Detail}`.
- `MapIs(err, target, code, status, detail)` — `errors.Is`.
- `MapAs[T](err, code, status, detail)` — `errors.As` по конкретному типу;
  `T = error` запрещён (паника), потому что совпал бы с любой ошибкой.

Незамапленная ошибка handler'а **не** уходит клиенту: любой `NewServer` один раз
подменяет `huma.NewErrorWithContext`, и на 5xx клиент получает нейтральное
`{"code":"INTERNAL","detail":"internal error"}`, а полный текст ошибки пишется в
лог (`handler error`) вместе с `request_id`. Ошибки валидации huma (4xx) хук не
трогает — их `errors[]` доходит до клиента как есть. Нейтрализация не зависит от
опций: `WithLogger` решает только, куда уходит текст (без него — `logkit.Nop()`,
причина 500 теряется). Хук — process-global переменная huma: логгером владеет
последний `Server`, которому его дали.

## Аутентификация

`HumaAuth(log, extract)` проверяет только операции с непустым
`Operation().Security` — гейт **opt-in**. Рекомендация: помечать `Security` все
непубличные операции явно, а публичные объявлять `Security: []map[string][]string{}`,
чтобы отсутствие тега не открывало эндпоинт молча (так сделаны `/health` и
`/ready`).

Ошибка extractor'а не уходит клиенту: `401 UNAUTHORIZED` (причина — в лог на
Debug), `*APIError` от extractor'а сохраняет свой статус и код, а ошибка,
обёрнутая в `httpkit.ErrAuthUnavailable` (JWKS/хаб/сессионный store недоступны),
даёт `503 AUTH_UNAVAILABLE` — деградация зависимости не должна разлогинивать
пользователя. Middleware работает с `huma.Context` (`*http.Request` достаётся
из адаптера или собирается из полей контекста), поэтому один и тот же гейт
тестируется и на prod-адаптере chi, и на `testkit/handlertest`.

## Идемпотентность

`Idempotency` буферизует тело запроса и ответа, резервирует ключ в
`IdempotencyStore` и воспроизводит сохранённый ответ на повтор. Модель:

- **Fingerprint** — метод + путь + канонизированный query + тело: `?dry_run=true`
  и «боевой» запрос с одним ключом дают `422`, а не чужой ответ.
- **Scope ключа** — аутентифицированный субъект (`httpkit.ContextWithSubject` в
  extractor'е), иначе хеш `Authorization` + `Cookie`. Субъект устойчив к ротации
  короткоживущего токена между попытками.
- **Replay — после аутентификации.** Когда в графе есть `AuthExtractor`,
  middleware вызывает его до обращения к store: отозванная сессия с тем же
  заголовком сохранённый ответ не получает. Успешная проверка помечает ctx,
  и `HumaAuth` не верифицирует те же credentials второй раз. Обратная сторона —
  анонимный запрос (extractor вернул ошибку) идёт **без дедупликации**
  (`result=skipped`): гарантия «выполнится один раз» есть только у
  аутентифицированного клиента.
- **Запись не зависит от клиента.** `Set`/`Release` идут с
  `context.WithoutCancel` и `StoreTimeout`: клиент отвалился по таймауту —
  результат всё равно сохранён, и его ретрай получает replay, а не второе
  выполнение.
- **Резерв снимает только владелец.** `Reserve` возвращает токен, `Release(ctx,
  key, token)` удаляет резерв, лишь пока ключ держит этот токен: запрос,
  переживший `ReserveTTL`, своим отложенным `Release` не открывает дорогу
  третьему запросу параллельно со вторым. Panic в handler'е освобождает ключ
  через `defer`, иначе ретраи получали бы `409` до конца `ReserveTTL`.
- Ответы `5xx` и ответы больше `MaxBodyBytes` не сохраняются. Ошибка store —
  fail-open (`HTTP_IDEMPOTENCY_FAIL_CLOSED=true` → `503`).

## Health / readiness

`RegisterHealth(api, log, probeTimeout, probes...)`:

- `GET /health` — liveness, всегда `{"status":"ok"}`.
- `GET /ready` — прогоняет пробы **параллельно**, каждую под своим таймаутом
  (`HTTP_READY_PROBE_TIMEOUT`): латентность readiness — максимум по пробам, а не
  сумма. Тело перечисляет пробы по имени со статусом `ok`/`down`, а текст ошибки
  уходит только в лог (`readiness probe failed`) — в нём бывают DSN и внутренние
  адреса.
