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

### Порядок хуков

fx выполняет `OnStart` в порядке добавления хуков, `OnStop` — в обратном; хук
добавляет конструктор или Invoke, который первым построил зависимость. `Module` —
это `fx.Module("httpkit", …)` и `fx.Invoke`, который вешает хук сервера на уровне,
куда включён `Module`, а fx выполняет Invoke всех `fx.Module` раньше Invoke их
уровня. Когда добавляется хук сервера, хуки пула, шины, кэша и доменов уже в
списке: HTTP начинает слушать после их `OnStart` и перестаёт принимать запросы до
их `OnStop`, поэтому запрос в полёте на остановке дорабатывает при живых
ресурсах, а свой `HTTP_SHUTDOWN_TIMEOUT` сервер берёт из общего бюджета
`fx.StopTimeout` первым. Место `Module` среди модулей не важно, два условия:

- `Module` — на верхнем уровне `fx.New`: внутри чужого `fx.Module` Invoke сервера
  выполнится раньше Invoke соседних модулей;
- корневой `fx.Invoke`, который строит зависимости, стоит до `Module` или живёт
  внутри `fx.Module`: Invoke одного уровня идут в порядке опций. Это касается и
  регистрации роутов: зависимость с хуком, которую первыми берут хендлеры
  (клиент внешнего API), строит Invoke регистрации — после `Module` её хук встал
  бы после хука сервера.

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
- `WithMiddleware(mw...)` / значение `httpkit.Middlewares` в графе — chi-middleware
  потребителя в порядке слайса (первый — внешний); в `Module` они встают
  **внутрь** цепочки кита, поэтому их panic ловит `Recovery`, а их ответы попадают
  в метрики и access-log. Значение одно на граф: зависимые middleware (tenant после
  client-IP) упорядочивает приложение, а не порядок сборки графа.
- `WithHumaConfig(fn)` / значение `httpkit.HumaConfigs` в графе — правки
  `huma.Config` до создания API (`Servers`, `DocsRenderer`, `Transformers`, свои
  `OnAddOperation`) в порядке слайса, после правок кита: хук потребителя видит
  операцию, которую кит уже поправил.
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
| 11 | middleware потребителя | значение `httpkit.Middlewares` | ближе всего к handler'у, в порядке слайса |

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
  причины 5xx, `ErrorLog`).
- `IdempotencyStore` (`optional`) — есть в графе → `Idempotency` с
  `Config.Idempotency` (`HTTP_IDEMPOTENCY_*`).
- `AuthExtractor` (`optional`) → `HumaAuth` и scope идемпотентности; нет, а у
  операции есть `Security`, — старт падает с `ErrAuthNotConfigured` и именами
  операций.
- `[]SecuritySchemeEntry` (группа `httpkit.security_scheme`) → OpenAPI.
- `Middlewares` (`optional`) → цепочка в порядке слайса.
- `HumaConfigs` (`optional`) → правки huma в порядке слайса.
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

## Контракт ошибок

Любой ответ-ошибка сервера — `APIError` с `Content-Type: application/problem+json`
(RFC 9457):

```json
{"title":"Unprocessable Entity","status":422,"detail":"validation failed",
 "code":"VALIDATION_FAILED","request_id":"01a0…","errors":[
   {"location":"body.password","code":"INVALID","message":"expected length >= 12"}]}
```

- `code` — стабильный машинный код: свой у операции (`404 USER_NOT_FOUND`) или
  общий кита по статусу — `400 BAD_REQUEST`, `401 UNAUTHORIZED`, `404 NOT_FOUND`,
  `405 METHOD_NOT_ALLOWED`, `413 BODY_TOO_LARGE`, `422 VALIDATION_FAILED`,
  `429 RATE_LIMITED`, `500 INTERNAL`, `503 UNAVAILABLE` (прочие статусы — текст
  статуса: `502 BAD_GATEWAY`).
- `request_id` равен `X-Request-ID` ответа и полю `request_id` строк лога запроса.
- `errors[]` — ошибки мест запроса `{location, code, message}`: у валидации huma
  (`code` — `INVALID`) и у `422 IDEMPOTENCY_KEY_REUSED` (`header.Idempotency-Key`).
  Значение поля клиенту не возвращается: в нём бывает пароль. Сообщения форматов
  huma, которые цитируют ввод (email, uri, uuid, regex, duration), обрезаются до
  базового текста (`expected string to be RFC 5322 email`).
- 5xx, собранная китом, — только `title`, `status`, `code` и `request_id`: текст
  причины и `detail` уходят в лог строкой `handler error` (`operation`, `status`,
  `code`, `err`).
- `Retry-After` в секундах (вверх) — из метода `RetryAfter() time.Duration` в
  цепочке причины; у 429 — не меньше секунды, даже без такой причины. Свой
  `Retry-After` хендлера (`huma.ErrorWithHeaders`) кит не перетирает.

Откуда берётся форма:

- **Ошибки huma.** Импорт `httpkit` в `init()` подменяет `huma.NewError` и
  `huma.NewErrorWithContext`: валидация, 5xx хендлера и `huma.Error4xx/5xx` —
  тоже `APIError`, во всём процессе, включая `humatest` в тестах потребителя.
  Нейтрализация 5xx не зависит от `Server` и опций.
- **Ответы huma.** Transformer сервера (`BindAPI`) видит каждое тело до записи
  статуса: ставит `request_id` и `Retry-After` в копию ошибки (общий
  package-level `APIError` не мутируется) и пишет причину 5xx в логгер
  `WithLogger` (без него — `logkit.Nop()`, причина теряется).
- **Ответы мимо huma** — `Recovery` (500), `RateLimit` (429), `Idempotency`,
  `HumaAuth` и маршрутизатор: неизвестный путь — `404 NOT_FOUND`, метод —
  `405 METHOD_NOT_ALLOWED` с `Allow`. Их пишет кит сам той же формой.

`APIError` реализует `error`, `huma.StatusError` и `huma.ContentTypeFilter`;
`huma.ErrorWithHeaders` дописывает к нему заголовки. Конструкторы —
`Error(code, status, detail)` и хелперы `BadRequest`…`Internal`, `Unavailable`.

Доменные ошибки переводит `ErrorMap` — одна таблица правил `ErrorRule` на домен
или операцию (`NewErrorMap` отвергает сломанную таблицу паникой): `Map` в
хендлере, `Declare` при регистрации операции (статусы — в `op.Errors`, коды — в
`op.Metadata`, откуда OnAddOperation сервера переносит их в описание ответа
статуса), `Unmapped` — в тесте полноты. `Map` сохраняет доменную ошибку причиной
`APIError`: из неё берётся `Retry-After`. Операция с `Security` объявляет ответ
`401 UNAUTHORIZED` (его отдаёт `HumaAuth`) сама; коды 401 из `Declare` — свои коды
extractor'а — дописываются в тот же ответ.

Вне контракта — ответы самого `net/http` на битый протокол (400, 431): их пишет
`http.Server` до цепочки. Повтор с `Idempotency-Key` воспроизводит тело оригинала
целиком, с его `request_id`: строки лога ответа — у исходного запроса.

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
пользователя.

Гейт fail-closed. Если в графе нет `AuthExtractor`, а у операции есть `Security`,
`Module` не стартует: на `OnStart` сервера, когда все Invoke уже зарегистрировали
операции, он обходит `OpenAPI().Paths` и возвращает `ErrAuthNotConfigured` с
именами таких операций. Скрытую операцию (`Hidden`) OpenAPI не видит, поэтому
`HumaAuth(log, nil)` сам закрывает защищённую операцию: `500 AUTH_NOT_CONFIGURED`
и строка `auth not configured` с `operation` в логе. Мимо гейта идут маршруты,
которые huma не регистрирует: сырой маршрут chi (`Router().Get`, `Mount`) и
`api.Adapter().Handle` — middleware API huma вешает только в `huma.Register`.

Middleware работает с `huma.Context` (`*http.Request` достаётся из адаптера или
собирается из полей контекста), поэтому один и тот же гейт тестируется и на
prod-адаптере chi, и на `testkit/handlertest`.

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
  строка `idempotency store failed` в логе и fail-open (`HTTP_IDEMPOTENCY_FAIL_CLOSED=true`
  → `503`).

## Health / readiness

`RegisterHealth(api, log, probeTimeout, probes...)`:

- `GET /health` — liveness, всегда `{"status":"ok"}`.
- `GET /ready` — прогоняет пробы **параллельно**, каждую под своим таймаутом
  (`HTTP_READY_PROBE_TIMEOUT`): латентность readiness — максимум по пробам, а не
  сумма. Тело перечисляет пробы по имени со статусом `ok`/`down`, а текст ошибки
  уходит только в лог (`readiness probe failed`) — в нём бывают DSN и внутренние
  адреса.
