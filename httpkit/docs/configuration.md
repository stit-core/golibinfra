# httpkit — конфигурация

`Config` агрегирует настройки сервера и вложенные конфиги middleware.
`NewConfig() (Config, error)` читает окружение ридером ENV китов ([корневой README § Стабильность API](../../README.md#стабильность-api)) и
вызывает `Config.Validate()`; `Module` валидирует ещё раз для конфигов, собранных
вручную. Ошибки чтения и валидации — `httpkit.ErrConfig`, они валят старт
приложения.

## Config

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_ADDR` | `Addr` | `string` | `:8080` | bind-адрес; `:0` — свободный порт (`Server.Addr()`) |
| `HTTP_READ_TIMEOUT` | `ReadTimeout` | `time.Duration` | `15s` | `http.Server.ReadTimeout`; `0` — без таймаута (net/http) |
| `HTTP_READ_HEADER_TIMEOUT` | `ReadHeaderTimeout` | `time.Duration` | `10s` | `http.Server.ReadHeaderTimeout` (slowloris); `0` — берётся `ReadTimeout`, оба `0` — без защиты (net/http) |
| `HTTP_WRITE_TIMEOUT` | `WriteTimeout` | `time.Duration` | `30s` | `http.Server.WriteTimeout`; `0` — без таймаута (net/http), для SSE ставьте `0` |
| `HTTP_IDLE_TIMEOUT` | `IdleTimeout` | `time.Duration` | `60s` | `http.Server.IdleTimeout`; `0` — берётся `ReadTimeout` (net/http) |
| `HTTP_REQUEST_TIMEOUT` | `RequestTimeout` | `time.Duration` | `25s` | дедлайн ctx запроса: ответ, не начатый к нему, — `503 REQUEST_TIMEOUT` (§ Дедлайн запроса); меньше `HTTP_WRITE_TIMEOUT` (если тот не `0`) и `HTTP_IDEMPOTENCY_RESERVE_TTL`; отрицательный (`-1s`) — без дедлайна, `0` — ошибка |
| `HTTP_SHUTDOWN_DRAIN_DELAY` | `ShutdownDrainDelay` | `time.Duration` | `0s` | пауза остановки: `/ready` отвечает `503`, listener ещё принимает запросы — балансировщик выводит инстанс; за балансировщиком (k8s) — 5–10 с; `>= 0` (§ Остановка) |
| `HTTP_SHUTDOWN_TIMEOUT` | `ShutdownTimeout` | `time.Duration` | `10s` | сколько после паузы дорабатывают запросы в полёте; на дедлайне их ctx отменяется, соединения закрываются; `> 0` (§ Остановка) |
| `HTTP_API_TITLE` | `APITitle` | `string` | `API` | title в OpenAPI; не пуст |
| `HTTP_API_VERSION` | `APIVersion` | `string` | `0.1.0` | version в OpenAPI; не пуст |
| `HTTP_DOCS_ENABLED` | `DocsEnabled` | `bool` | `false` | `true` отдаёт `/docs`, `/openapi.json`, `/openapi.yaml`, `/schemas` без аутентификации — карта всех операций; для dev и внутреннего контура |
| `HTTP_READY_PROBE_TIMEOUT` | `ReadyProbeTimeout` | `time.Duration` | `5s` | потолок одной пробы `/ready`; `> 0` |
| `HTTP_QUIET_PATHS` | `QuietPaths` | `[]string` | `/health,/ready` | пути без Info-строки access-log и без server-спана; `-` — таких путей нет |
| `HTTP_TRUST_PROXY_HOPS` | `TrustProxyHops` | `int` | `0` | сколько доверенных прокси перед сервисом дописывают `X-Forwarded-For` (§ IP клиента); `>= 0` |
| `HTTP_TRUST_TRACE_CONTEXT` | `TrustTraceContext` | `bool` | `false` | `true` — `traceparent` и `baggage` клиента принимаются: server-спан — ребёнок входящего контекста; `false` — ссылка на спане нового корня, baggage не читается. `true` — только за доверенным краем (свой шлюз), иначе клиент выбирает trace_id, сэмплирование и значения baggage |
| — | `CORS` | `CORSConfig` | — | вложенный |
| — | `RateLimit` | `RateLimitConfig` | — | вложенный |
| — | `MaxBody` | `MaxBodyConfig` | — | вложенный |
| — | `Security` | `SecurityHeadersConfig` | — | вложенный |
| — | `Idempotency` | `IdempotencyConfig` | — | вложенный |

## CORSConfig

No-op при пустом `Origins`. Конкретный origin эхается с `Vary: Origin`; origin,
разрешённый только по `*`, всегда получает `*` **без** credentials — отражать
произвольный origin вместе с credentials небезопасно.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_CORS_ORIGINS` | `Origins` | `[]string` | — (CORS выключен) | разрешённые Origin; `*` = любой |
| `HTTP_CORS_METHODS` | `Methods` | `[]string` | `GET,POST,PUT,PATCH,DELETE,OPTIONS` | `Access-Control-Allow-Methods` |
| `HTTP_CORS_HEADERS` | `Headers` | `[]string` | `Authorization,Content-Type,X-Request-ID,Idempotency-Key` | `Access-Control-Allow-Headers`; свой `HTTP_IDEMPOTENCY_HEADER` — добавьте его сюда, иначе браузер не отправит ключ |
| `HTTP_CORS_EXPOSE_HEADERS` | `ExposeHeaders` | `[]string` | `X-Request-ID,Location,Retry-After,Idempotent-Replayed` | `Access-Control-Expose-Headers`: без него cross-origin JS не читает даже заголовки, которые ставит сам кит |
| `HTTP_CORS_MAX_AGE` | `MaxAge` | `int` | `600` | `Access-Control-Max-Age` (сек); `>= 0` |
| `HTTP_CORS_ALLOW_CREDENTIALS` | `AllowCredentials` | `bool` | `false` | `Access-Control-Allow-Credentials: true`; несовместим с `*` (ошибка `Validate`) |

## IP клиента

`ClientIP` (в цепочке `Module` — сразу после `RequestID`, до `OTel`) кладёт IP клиента в ctx:
`httpkit.ClientIPFrom(ctx)` — один адрес для лимитера, `client.address` server-спана,
`bruteforcekit` и аудита. Каждый доверенный прокси дописывает адрес своего собеседника в
конец `X-Forwarded-For` (`$proxy_add_x_forwarded_for` у nginx, так же Traefik), а всё левее
прислал сам клиент:

- `HTTP_TRUST_PROXY_HOPS=0` — сервис без прокси: адрес соединения, заголовки не читаются;
  пришедший `X-Forwarded-For` — одна строка Warn (за прокси без настройки все клиенты делят
  его адрес, а с ним bucket лимитера и ключ антибрутфорса);
- `1` — один прокси (Traefik, nginx): правая запись; `2` — CDN → nginx: вторая справа.

Записи всех заголовков `X-Forwarded-For` идут по порядку; запись с портом (`203.0.113.7:4711`,
`[2001:db8::7]:443`) — её IP; цепочка короче `N` или запись — не IP: адрес соединения. Тест
usecase'а кладёт IP без middleware — `httpkit.ContextWithClientIP(ctx, ip)`. `X-Real-IP` не читается: прокси, который ставит только его, настраивают
дописывать `X-Forwarded-For`. Адрес — в канонической форме (`::ffff:203.0.113.7` →
`203.0.113.7`, `2001:DB8::1` → `2001:db8::1`).

## RateLimitConfig

Token-bucket на клиента (`golang.org/x/time/rate`): ключ — IP из `ClientIP` (без него в
цепочке — адрес соединения), IPv6 — по подсети `/64`, как у `bruteforcekit`. Превышение →
`429 RATE_LIMITED` с `Retry-After: 1` и `application/problem+json`. Простаивающие bucket'ы
выметаются (5 минут без запросов); bucket'ов не больше 100 000 (≈17 MiB) — сверх предела
новые клиенты до выметания делят один общий bucket.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_RATE_LIMIT_RPS` | `RPS` | `int` | `100` | запросов в секунду на клиента; `0` — off |
| `HTTP_RATE_LIMIT_BURST` | `Burst` | `int` | `200` | ёмкость bucket; `0` → равна `RPS` |

## MaxBodyConfig

Оборачивает `r.Body` в `http.MaxBytesReader`. Превышение при чтении даёт
`*http.MaxBytesError` (`IsMaxBodyError`), ответ `413 BODY_TOO_LARGE` считается в
`http_max_body_rejections_total`.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_MAX_BODY_BYTES` | `MaxBytes` | `int64` | `1048576` (1 MiB) | лимит тела; `0` — off; `>= 0` |

Значение доезжает и до huma: `BindAPI` ставит `Operation.MaxBodyBytes` каждой
операции, в том числе скрытой (иначе huma резал бы тело собственным дефолтом 1 MiB
независимо от `HTTP_MAX_BODY_BYTES`). Отдельной операции лимит меняется своим хуком
`WithHumaConfig` — он применяется после кита и выигрывает. Лимит `MaxBytesReader`
фиксируется на первом чтении тела, и тело операции huma читается по её
`MaxBodyBytes`: поднятый лимит поднимает и лимит транспорта, у маршрутов мимо huma —
`HTTP_MAX_BODY_BYTES`. Превышение — всегда `413 BODY_TOO_LARGE`, и в huma тоже.

## SecurityHeadersConfig

Заголовки на каждый ответ; пустое значение поля (в ENV — `-`) — заголовок не ставится.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_HSTS` | `HSTS` | `string` | `max-age=31536000; includeSubDomains` | `Strict-Transport-Security` |
| `HTTP_CONTENT_TYPE_OPTIONS` | `ContentTypeOptions` | `string` | `nosniff` | `X-Content-Type-Options` |
| `HTTP_FRAME_OPTIONS` | `FrameOptions` | `string` | `DENY` | `X-Frame-Options` |
| `HTTP_REFERRER_POLICY` | `ReferrerPolicy` | `string` | `strict-origin-when-cross-origin` | `Referrer-Policy` |
| `HTTP_CSP` | `CSP` | `string` | `""` | `Content-Security-Policy` |

## IdempotencyConfig

Читается из ENV и применяется `Module`, когда в графе есть `IdempotencyStore`: `Idempotency`
стоит в цепочке huma после `HumaAuth` — у операций huma.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_IDEMPOTENCY_HEADER` | `Header` | `string` | `Idempotency-Key` | заголовок с ключом |
| `HTTP_IDEMPOTENCY_TTL` | `TTL` | `time.Duration` | `24h` | срок хранения ответа; `> 0` |
| `HTTP_IDEMPOTENCY_RESERVE_TTL` | `ReserveTTL` | `time.Duration` | `60s` | сколько ключ считается «выполняется», если процесс упал в середине запроса; больше `HTTP_REQUEST_TIMEOUT` (резерв переживает запрос) |
| `HTTP_IDEMPOTENCY_MAX_BODY_BYTES` | `MaxBodyBytes` | `int64` | `65536` | потолок буферизации запроса и ответа; больше — запрос идёт без идемпотентности; `> 0` |
| `HTTP_IDEMPOTENCY_STORE_TIMEOUT` | `StoreTimeout` | `time.Duration` | `2s` | потолок одной операции store; запись результата не зависит от отмены ctx клиента; `> 0` |
| `HTTP_IDEMPOTENCY_FAIL_CLOSED` | `FailClosed` | `bool` | `false` | `true` — недоступный store отвечает `503 IDEMPOTENCY_UNAVAILABLE`; `false` — запрос выполняется без дедупликации (fail-open) |

## Дедлайн запроса

`HTTP_REQUEST_TIMEOUT` — дедлайн ctx запроса (middleware над `Idempotency`): запросы в
БД и к другим сервисам с этим ctx обрываются на нём. Ответ, не начатый к дедлайну, — сразу
`503 REQUEST_TIMEOUT` problem+json и WARN `request deadline exceeded`: клиент получает
ошибку контракта, а не обрыв соединения по `HTTP_WRITE_TIMEOUT`, поэтому дедлайн меньше
него. 503 — целый ответ (`Content-Length`) с `Connection: close`: клиент дочитывает его, не
дожидаясь хендлера, а следующий запрос шлёт в новое соединение — это хендлер держит до
возврата. Запись хендлера после 503 молча отбрасывается (признак — ctx запроса), а сам он
дорабатывает: `Idempotency` держит резерв до его возврата и сохраняет его ответ — повтор с
тем же ключом получает результат, а не второе выполнение (ответ `5xx` не хранится —
повтор исполняет заново). Ошибка хендлера после дедлайна — строка `handler error` в логе,
клиенту уже ушёл 503. Начатый к дедлайну ответ дописывается как есть.

Без дедлайна идут потоки — GET с `Accept: text/event-stream` (SSE) или `Upgrade`
(WebSocket). Заголовок шлёт клиент, поэтому и GET без потока он так снимает с дедлайна: GET
не меняет состояния, и повтор его безопасен. Запись (POST, PUT, PATCH, DELETE) дедлайн
получает всегда — поток на POST обрывается на нём.

## Остановка

`OnStop` сервера (и `Server.Stop` без fx):

1. `/ready` сразу отвечает `503 {"status":"draining"}`;
2. пауза `HTTP_SHUTDOWN_DRAIN_DELAY` — listener открыт, запросы обслуживаются, а
   балансировщик по `/ready` выводит инстанс;
3. listener закрывается, запросы в полёте дорабатывают ещё
   `HTTP_SHUTDOWN_TIMEOUT`;
4. на дедлайне ctx оставшихся хендлеров отменяется (и у захваченных соединений,
   WebSocket), соединения закрываются, WARN `http server stop deadline: requests
   cancelled, connections closed` называет их число.

Шаги 2–4 укладываются и в две трети остатка бюджета остановки (`fx.StopTimeout`):
треть остаётся хукам после HTTP — drain подписок natskit, остановке job'ов,
закрытию пула, финальному экспорту telemetry, — даже если долгий запрос или
SSE-поток держит соединение. Чтобы HTTP не упирался в этот потолок, держите
`HTTP_SHUTDOWN_DRAIN_DELAY + HTTP_SHUTDOWN_TIMEOUT` не больше двух третей
`fx.StopTimeout`, а сумму с `NATS_DRAIN_TIMEOUT` и прочими бюджетами хуков —
не больше `fx.StopTimeout`; оркестратор ждёт дольше `fx.StopTimeout`
(`terminationGracePeriodSeconds`, `stop_grace_period`). Дефолты — `0s` и `10s` при
`fx.StopTimeout` по умолчанию `15s`.

## Правила Validate

- `Addr` не пуст; таймауты `>= 0`; `ShutdownTimeout > 0`; `ShutdownDrainDelay >= 0`;
  `ReadyProbeTimeout > 0`; `TrustProxyHops >= 0`.
- `RequestTimeout` не `0`; положительный — меньше `WriteTimeout` (если тот не `0`) и
  меньше `Idempotency.ReserveTTL`.
- `APITitle` и `APIVersion` не пусты.
- `RateLimit.RPS`, `RateLimit.Burst`, `MaxBody.MaxBytes`, `CORS.MaxAge` `>= 0`.
- `Idempotency`: `Header` не пуст, `TTL`, `ReserveTTL`, `MaxBodyBytes`,
  `StoreTimeout` `> 0`.
- `CORS.AllowCredentials` несовместим с origin `*`.

Zero-value `Config` отвергается (`Addr` пуст) и в `Module`, и в `NewServer` (`ErrConfig`) —
сервер стартует только на осознанно заданных значениях.

## Public API

Имена полей и envconfig-теги всех перечисленных структур — part of public API
(корневой [README](../../README.md) § Стабильность API).
