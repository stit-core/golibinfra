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
`ErrorLog` сервера направлен в `logkit` (логгер `NewServer`), поэтому ошибки
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
ресурсах. Остановка HTTP берёт не больше двух третей остатка бюджета
`fx.StopTimeout`: на дедлайне ctx хендлеров отменяется, а соединения закрываются,
и хуки после HTTP получают свою треть (порядок остановки — [configuration.md §
Остановка](configuration.md#остановка)). Место `Module` среди модулей не важно,
два условия:

- `Module` — на верхнем уровне `fx.New`: внутри чужого `fx.Module` Invoke сервера
  выполнится раньше Invoke соседних модулей;
- корневой `fx.Invoke`, который строит зависимости с хуком или вешает хук сам
  (`lc.Append` воркера), стоит до `Module` или живёт внутри `fx.Module`: Invoke
  одного уровня идут в порядке опций. Это касается и регистрации роутов:
  зависимость с хуком, которую первыми берут хендлеры (клиент внешнего API),
  строит Invoke регистрации — после `Module` её хук встал бы после хука сервера.

## Ленивый BindAPI — порядок имеет значение

`huma.API` создаётся не в `NewServer`, а лениво в `BindAPI()`: `humachi.New`
сразу регистрирует `/openapi.json` и `/docs`, после чего chi запрещает
`router.Use(...)`. Контракт:

1. `NewServer(cfg, log, opts...)` — проверка `Config` и схем, router + `*http.Server`, цепочка кита
   (таблица ниже) и middleware из `WithMiddleware` внутри неё. Эту же функцию зовёт `Module`.
2. `router.Use(middleware...)` — до `BindAPI` можно добавить ещё middleware внутрь цепочки.
3. `BindAPI()` — создаёт `huma.API` и ставит лимит тела операции и `HumaAuth` первыми
   huma-middleware.
4. `huma.Register` / `huma.Get`; `api.UseMiddleware(...)` для huma-уровня.

`BindAPI` идемпотентен и безопасен из нескольких горутин, `API()` — алиас. `Module` соблюдает
порядок сам.

### Точки расширения

- `WithSecuritySchemes(entries...)` / `ProvideSecurityScheme(ctor)` (fx-группа
  `httpkit.security_scheme`) — `components.securitySchemes`; дефолтных схем httpkit не
  выставляет.
- `WithMiddleware(mw...)` / значение `httpkit.Middlewares` в графе — chi-middleware
  потребителя в порядке слайса (первый — внешний); в `Module` они встают
  **внутрь** цепочки кита, поэтому их panic ловит `Recovery`, а их ответы попадают
  в метрики и access-log. Значение одно на граф: зависимые middleware (аудит после
  tenant) упорядочивает приложение, а не порядок сборки графа.
- `WithHumaConfig(fn)` / значение `httpkit.HumaConfigs` в графе — правки
  `huma.Config` до создания API (`Servers`, `DocsRenderer`, `Transformers`, свои
  `OnAddOperation`) в порядке слайса, после правок кита: хук потребителя видит
  операцию, которую кит уже поправил (лимит тела). Описание ошибок (коды `Declare` и
  ответы кита — § Контракт ошибок) кит ставит последним хуком — оно видит и
  `Security` и лимит тела, выставленные хуком потребителя. Правки хуков исполняет и хендлер
  операции, в том числе из группы huma (§ Аутентификация).
- `WithPublicRoutes(paths...)` / значение `httpkit.PublicRoutes` в графе — пути chi
  маршрутов мимо `huma.Register`, открытых без аутентификации (§ Аутентификация).
- `HTTP_DOCS_ENABLED=true` — `/docs`, `/openapi.*` и `/schemas/*` (по умолчанию их нет: это карта
  всех операций без аутентификации).

## Конвейер middleware и порядок

chi применяет middleware в порядке регистрации: первый в списке — внешний,
запрос идёт сверху вниз, ответ — снизу вверх.

| # | Middleware | Условие | Зачем именно здесь |
|---|---|---|---|
| 1 | `RequestID` | всегда | `X-Request-ID` в ctx до всего остального: он попадает в каждую строку лога и в атрибут спана |
| 2 | `ClientIP` | всегда | IP клиента в ctx (`ClientIPFrom`, `HTTP_TRUST_PROXY_HOPS`) до спана: ключ лимитера, `client.address`, аудит и антибрутфорс видят один адрес; `r.WithContext` выше `OTel` не трогает объект запроса otelhttp |
| 3 | `OTel` | всегда | server-span; всё ниже работает внутри спана, поэтому `trace_id` есть в access-log и panic-log. `traceparent` клиента — ссылка на спане нового корня, baggage не читается (клиент не выбирает trace_id и сэмплирование); `HTTP_TRUST_TRACE_CONTEXT=true` за доверенным краем — спан продолжает трассу клиента. Без SDK-провайдера трасс (`OTEL_SDK_DISABLED=true`) спана нет: контекст клиента без доверия дальше `OTel` не идёт, с доверием — идёт как пришёл, и `trace_id` в логах есть только у такого запроса |
| 4 | `AccessLog` | всегда | строка на запрос с `route`, `status`, `bytes`, `duration_ms` |
| 5 | `Metrics` | всегда | RED на chi route-pattern; имя спана и `client.address`; ниже него — всё, что может ответить клиенту |
| 6 | `Recovery` | всегда | ловит panic из всего, что ниже: 500 + `http_panics_total`; после записанного заголовка — обрыв соединения (`http.ErrAbortHandler`): усечённый ответ не выглядит полным; свой `http.ErrAbortHandler` хендлера пробрасывает |
| 7 | `SecurityHeaders` | всегда | заголовки на любой ответ, включая 429 и 413 |
| 8 | `CORS` | если `CORS.Origins` не пуст | preflight отвечает 204 до лимитера и тела |
| 9 | `RateLimit` | если `RateLimit.RPS > 0` | bucket на клиента (IPv6 — по `/64`); дешёвый отказ до тяжёлой работы |
| 10 | `MaxBody` | если `MaxBody.MaxBytes > 0` | `MaxBytesReader` до чтения body handler'ом; лимит фиксируется на первом чтении — операция huma ставит свой `MaxBodyBytes` |
| 11 | дедлайн запроса | если `RequestTimeout > 0`; GET-поток (операция SSE, upgrade мимо huma) — без него | ctx с дедлайном и `503 REQUEST_TIMEOUT` за не начатый к нему ответ, запись хендлера после него отбрасывается; над `Idempotency` — она видит ответ хендлера и держит резерв до его возврата ([configuration.md § Дедлайн запроса](configuration.md#дедлайн-запроса)) |
| 12 | HEAD как GET | всегда | HEAD без своего маршрута идёт в GET-маршрут (RFC 9110 §9.1), тело net/http не отправляет |
| 13 | middleware потребителя | `WithMiddleware`, в графе — значение `httpkit.Middlewares` | ближе всего к handler'у, в порядке слайса |

Лимит тела операции, `HumaAuth` и `Idempotency` (если есть `IdempotencyStore`) — отдельный
слой на `huma.API`, в этом порядке, до middleware huma потребителя: идемпотентность знает
операцию и субъекта.

Почему так: `RequestID`, `ClientIP` и `OTel` первыми, чтобы контекст запроса был полным у
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
  `Config.Idempotency` (`HTTP_IDEMPOTENCY_*`) в цепочке huma после `HumaAuth`.
- `[]SecuritySchemeEntry` (группа `httpkit.security_scheme`) → OpenAPI и extractor'ы
  `HumaAuth` (`Extract`; у схемы операции его нет — старт падает с `ErrAuthNotConfigured`). Дубль имени и
  схема без `Name` или `Scheme` — ошибка сборки графа (`ErrConfig`): из группы значения
  приходят в случайном порядке, и в OpenAPI попала бы случайная из двух.
- `Middlewares` (`optional`) → цепочка в порядке слайса.
- `HumaConfigs` (`optional`) → правки huma в порядке слайса.
- `PublicRoutes` (`optional`) → открытые маршруты мимо `huma.Register`.
- `[]healthkit.Probe` (группа `healthkit.probes`) → `/ready`; две пробы с одним именем —
  ошибка сборки графа (`ErrConfig`). Тип именованный:
  `httpkit.Probe` — alias `healthkit.Probe`, а `pgkit.Probe`, `natskit.Probe`,
  `cachekit.Probe` возвращают его же. Регистрация — `healthkit.ProvideProbe(ctor)`.

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
  `429 RATE_LIMITED`, `500 INTERNAL`, `503 UNAVAILABLE`, `503 REQUEST_TIMEOUT` (дедлайн
  запроса; прочие статусы — текст статуса: `502 BAD_GATEWAY`).
- `request_id` равен `X-Request-ID` ответа и полю `request_id` строк лога запроса.
- Других полей нет: `$schema` в теле и `Link` в ответе huma не ставит — его link-трансформер
  снят (ссылка строилась из `X-Forwarded-Host` клиента и без docs вела в 404).
- `errors[]` — ошибки мест запроса `{location, code, message}`: у валидации huma
  (`code` — `INVALID`) и у `422 IDEMPOTENCY_KEY_REUSED` (`header.Idempotency-Key`).
  Значение поля клиенту не возвращается: в нём бывает пароль. Сообщения huma,
  которые цитируют ввод, обрезаются до базового текста — форматы (email, uri,
  uuid, regex, duration: `expected string to be RFC 5322 email`) и ошибка
  `UnmarshalText` параметра (`invalid value`). Исключение — текст декодера тела
  с location `body`: синтаксис JSON (называет один символ) и ошибку
  `UnmarshalText`/`UnmarshalJSON` типа поля, которую ловит не схема, а разбор
  тела (`netip.Prefix`), huma отдаёт как есть. Строковое поле, где бывает
  пароль, проверяет схема, а не декодер. Имена из запроса возвращаются: `location`
  называет его ключ, и лишний тоже (`body.<ключ>`, `unexpected property`), а `415` —
  его `Content-Type` (`unknown content type: …`).
- 5xx, собранная китом, — только `title`, `status`, `code` и `request_id`: текст
  причины и `detail` уходят в лог строкой `handler error` (`operation`, `status`,
  `code`, `error`).
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
  `NewServer` (с `logkit.Nop()` причина теряется).
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
`APIError`: из неё берётся `Retry-After`.

Ответы, которые кит отдаёт сам, операция объявляет в OpenAPI сама — там и тогда, где он их
отдаёт; коды того же статуса из `Declare` идут первыми, код кита — после них и без повтора:

- `401 UNAUTHORIZED` (`HumaAuth`) — у операции huma с обязательной аутентификацией: `Security`
  без пустой альтернативы, в том числе выставленный хуком потребителя; коды 401 из `Declare` —
  свои коды extractor'а — в тот же ответ. Заголовок `WWW-Authenticate` — вызов схем операции
  (он же — пример в OpenAPI), необязательный: свой 401 хендлера его не несёт. При
  необязательной аутентификации (`{}`) и у маршрута мимо huma 401 кит не отдаёт.
- `429 RATE_LIMITED` (`RateLimit`) при `HTTP_RATE_LIMIT_RPS > 0` — у каждой операции, и у
  `/health`, `/ready`, и у описанной в OpenAPI мимо `huma.Register`. У любого 429 операции —
  обязательный `Retry-After` (целые секунды, от 1): кит ставит его на каждый ответ 429.
- `503 REQUEST_TIMEOUT` (дедлайн запроса) при `HTTP_REQUEST_TIMEOUT > 0` — у каждой операции,
  кроме потока SSE (ответ `text/event-stream`); у ответа операции со своим телом того же статуса
  (503 `/ready`, § Health / readiness) `APIError` описан рядом с ним.
- `413 BODY_TOO_LARGE` — у операции huma с телом при лимите тела (`MaxBodyBytes > 0`); у операции
  группы huma с префиксом — и при лимите, с которым тело читает сама huma
  ([configuration.md § MaxBodyConfig](configuration.md#maxbodyconfig)). У multipart-формы
  превышение — `422 VALIDATION_FAILED`: форму huma разбирает сама; у маршрута мимо huma на
  превышение отвечает его хендлер (`IsMaxBodyError`).

`503 AUTH_UNAVAILABLE` отдаёт не каждая схема, а extractor, который знает недоступный бэкенд
(JWKS, хаб): его объявляет владелец схемы правилом таблицы операций схемы —
`ErrorRule{Err: httpkit.ErrAuthUnavailable, Status: 503, Code: "AUTH_UNAVAILABLE"}`. Так же
свой лимитер потребителя (`RateLimit` на части путей) объявляет свой 429: при включённом
лимитере сервера код не повторится, а при выключенном 429 в контракте останется.

Вне контракта — ответы самого `net/http` на битый протокол (400, 431): их пишет
`http.Server` до цепочки. Повтор с `Idempotency-Key` воспроизводит тело оригинала
целиком, с его `request_id`: строки лога ответа — у исходного запроса.

## Аутентификация

`HumaAuth(schemes, log)` проверяет операции с непустым `Operation().Security` extractor'ами их
схем (`SecuritySchemeEntry.Extract`): операция открыта, если прошла одна из её альтернатив
целиком — каждая схема альтернативы, в порядке имён, и личность в ctx — от неё; Bearer не
открывает операцию под `AdminKey`. Пустая альтернатива (`Security: [{bearer}, {}]`) открывает
операцию и без credentials — необязательная аутентификация OpenAPI; она идёт последней, где
бы ни стояла: прошедшие схему credentials дают личность, отказ бэкенда схемы — 503, остальное —
запрос без личности. Чтобы забытый `Security` не открывал операцию молча, открытость явная:
когда в `components.securitySchemes` есть схема, у каждой операции — `Security` со схемой из
`components` или пометка `httpkit.Public(op)` (в `Operation.Metadata`); пустой `Security`
публичным не считается, а `Security` из одних `{}` — ошибка старта: открытая операция —
только `Public`. Так сделаны `/health` и `/ready`. Без схем в графе (сервис без
аутентификации) пометка не нужна.

Ошибка extractor'а не уходит клиенту: `401 UNAUTHORIZED` (причина — в лог на
Debug), `*APIError` от extractor'а сохраняет свой статус и код, а ошибка,
обёрнутая в `httpkit.ErrAuthUnavailable` (JWKS/хаб/сессионный store недоступны),
даёт `503 AUTH_UNAVAILABLE` — деградация зависимости не должна разлогинивать
пользователя. 401 несёт `WWW-Authenticate` (RFC 9110 §11.6.1) со схемами операции из
`components`: `http` — своей схемой (`Bearer`, `Basic`), `oauth2` и `openIdConnect` —
`Bearer`, `apiKey` — именем схемы; альтернативы — через запятую. Вызов считает хук кита
`OnAddOperation`; у операции чужого API (`HumaAuth` на своём huma) — имена схем.

Гейт fail-closed, проверка — на `OnStart` сервера, когда все Invoke уже
зарегистрировали операции. Видит она каждую операцию `huma.Register`, в том числе
скрытую (`Hidden`) и из группы huma: API сервера — `huma.OperationDocumenter`, а
`OnAddOperation` и `OpenAPI().Paths` скрытых не видят. Хендлер исполняет ту же копию
операции, что видят проверка и OpenAPI: группа huma с префиксом отдаёт хендлеру свою копию
без правок хуков `OnAddOperation`, и кит ставит вместо неё копию из OpenAPI — `Security`
хука закрывает и операцию группы, вызов 401 и лимит тела кита — по её схемам и
`MaxBodyBytes`. `CheckRoutes` возвращает
`ErrRoutes` с именами операций и маршрутов: два одинаковых METHOD+path (`/users/{id}` и
`/users/{userID}` для chi — один маршрут, второй молча заменил бы первый), маршрут мимо
`huma.Register` на METHOD+path операции — в любом порядке: chi оставляет маршруту последний
хендлер, и чужой открыл бы операцию мимо `HumaAuth` (хендлер операции кит ставит своего
типа и по нему отличает), `Public` с `Security`, `Security` из одних `{}`; при схеме в
графе — операция без `Security` и без `Public`, схема в `Security` вне `components`, маршрут
мимо `huma.Register` (`Router().Get`, `Mount`, `api.Adapter().Handle` — middleware API huma, а с
ним `HumaAuth`, вешает только `huma.Register`), путь которого не в `PublicRoutes`, и строка
`PublicRoutes` без такого маршрута. Маршруты документации huma (`/docs`, `/openapi.*`,
`/schemas/*`) — свои у кита. Маршрут, добавленный после проверки — в `OnStart` позже хука
сервера или из хендлера, — она не видит: маршруты регистрируют в Invoke или конструкторе.
Схема в `Security` вне `components` — ошибка проверки и без схем в графе (OpenAPI был бы
невалиден). Затем, если у схемы операции нет `Extract`, `Module` не стартует с
`ErrAuthNotConfigured` и парами «операция (схема)». Без fx `CheckRoutes` зовут сами, а
`HumaAuth` на операции, которую открыть нечем, отвечает `500 AUTH_NOT_CONFIGURED` со строкой
`auth not configured` и `operation` в логе. Свой `Router().NotFound` маршрутом не считается:
проверка его не видит, и он отвечает без аутентификации — контент там не отдают.

Middleware работает с `huma.Context` (`*http.Request` достаётся из адаптера или
собирается из полей контекста), поэтому один и тот же гейт тестируется и на
prod-адаптере chi, и на `testkit/handlertest`.

## Идемпотентность

`Idempotency` буферизует тело запроса и ответа, резервирует ключ в
`IdempotencyStore` и воспроизводит сохранённый ответ на повтор. Модель:

- **Fingerprint** — метод + путь + канонизированный query + тело: `?dry_run=true`
  и «боевой» запрос с одним ключом дают `422`, а не чужой ответ.
- **Scope ключа** — субъект запроса (`httpkit.ContextWithSubject` в extractor'е схемы): он
  устойчив к ротации короткоживущего токена между попытками. Запрос без субъекта — аноним с
  любыми `Authorization` и `Cookie` и клиент, чей extractor субъекта не положил, — идёт без
  дедупликации (`result=skipped`) и в store не пишет: общий scope отдал бы сохранённый ответ
  одного клиента другому, а scope по credentials менялся бы с каждым токеном.
- **Бюджет субъекта** — не больше 10 000 сохранённых ответов на субъекта за окно `TTL`, счёт в
  памяти процесса: новый ключ сверх него — отказ store этому клиенту, а не всем.
- **Replay — после аутентификации.** `Idempotency` стоит в цепочке huma после `HumaAuth`:
  у защищённой операции отозванная сессия получает 401, а не сохранённый ответ, extractor
  зовётся один раз на запрос, а replay учитывается метриками по маршруту операции.
  Идемпотентность — у операций huma; маршрутам мимо huma её нет.
- **Запись не зависит от клиента.** `Set`/`Release` идут с
  `context.WithoutCancel` и `StoreTimeout`: клиент отвалился по таймауту —
  результат всё равно сохранён, и его ретрай получает replay, а не второе
  выполнение.
- **Резерв и запись — только у владельца.** `Reserve` возвращает токен;
  `Set(ctx, key, token, …)` кладёт ответ, а `Release(ctx, key, token)` удаляет
  резерв, лишь пока ключ держит этот токен. Запрос, переживший `ReserveTTL`, не
  затирает резерв повтора, который уже занял ключ, и не открывает дорогу третьему
  запросу параллельно со вторым: его ответ не сохраняется — строка
  `idempotency reservation expired before the response` и `result=skipped`. Panic
  в handler'е освобождает ключ через `defer`, иначе ретраи получали бы `409` до
  конца `ReserveTTL`.
- Ответы `5xx`, ответы больше `MaxBodyBytes` и «не выполнено, повторите» — `408`, `409`,
  `425`, `429` — не сохраняются: повтор с тем же ключом исполняет запрос заново, а не
  получает отказ на `HTTP_IDEMPOTENCY_TTL`. Ошибка store —
  строка `idempotency store failed` в логе и fail-open (`HTTP_IDEMPOTENCY_FAIL_CLOSED=true`
  → `503`).

## Health / readiness

`RegisterHealth(api, probeTimeout, log, probes...)`:

- `GET /health` — liveness, всегда `{"status":"ok"}`.
- `GET /ready` — прогоняет пробы **параллельно**, каждую под своим таймаутом
  (`HTTP_READY_PROBE_TIMEOUT`): латентность readiness — максимум по пробам, а не
  сумма. Тело перечисляет пробы по имени со статусом `ok`/`down`, а текст ошибки
  уходит только в лог (`readiness probe failed`) — в нём бывают DSN и внутренние
  адреса. panic пробы — тоже `down` и ERROR `readiness probe panicked` со стеком
  (`healthkit.Probe.Run` превращает его в `*healthkit.PanicError`): пробы идут в
  своих горутинах, и `Recovery` их не видит.
- С начала остановки `/ready` отвечает `503 {"status":"draining"}` без проб: флаг
  остановки сервер кладёт в базовый ctx запросов, поэтому так отвечает `/ready`,
  зарегистрированный и без fx, на API этого сервера.
- В OpenAPI у `/ready` ответ 503 — тело готовности `ReadyBody` (`application/json`,
  описание перечисляет его `status`: `draining`, `unhealthy`), а при дедлайне запроса рядом —
  `APIError` дедлайна (`503 REQUEST_TIMEOUT`, `application/problem+json`): `Service Unavailable:
  draining, unhealthy, REQUEST_TIMEOUT`.
