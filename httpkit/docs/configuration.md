# httpkit — конфигурация

`Config` агрегирует настройки сервера и вложенные конфиги middleware.
`NewConfig() (Config, error)` читает окружение через envconfig и вызывает
`Config.Validate()`; `Module` валидирует ещё раз для конфигов, собранных вручную.
Ошибки валидации — `httpkit.ErrConfig`, они валят старт приложения.

## Config

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_ADDR` | `Addr` | `string` | `:8080` | bind-адрес; `:0` — свободный порт (`Server.Addr()`) |
| `HTTP_READ_TIMEOUT` | `ReadTimeout` | `time.Duration` | `15s` | `http.Server.ReadTimeout` |
| `HTTP_READ_HEADER_TIMEOUT` | `ReadHeaderTimeout` | `time.Duration` | `10s` | `http.Server.ReadHeaderTimeout` (slowloris) |
| `HTTP_WRITE_TIMEOUT` | `WriteTimeout` | `time.Duration` | `30s` | `http.Server.WriteTimeout`; для SSE ставьте `0` |
| `HTTP_IDLE_TIMEOUT` | `IdleTimeout` | `time.Duration` | `60s` | `http.Server.IdleTimeout` |
| `HTTP_SHUTDOWN_TIMEOUT` | `ShutdownTimeout` | `time.Duration` | `15s` | дедлайн graceful-shutdown на `OnStop`; `> 0` |
| `HTTP_API_TITLE` | `APITitle` | `string` | `API` | title в OpenAPI; не пуст |
| `HTTP_API_VERSION` | `APIVersion` | `string` | `0.1.0` | version в OpenAPI; не пуст |
| `HTTP_DOCS_ENABLED` | `DocsEnabled` | `bool` | `true` | `false` убирает `/docs`, `/openapi.json`, `/openapi.yaml`, `/schemas` |
| `HTTP_READY_PROBE_TIMEOUT` | `ReadyProbeTimeout` | `time.Duration` | `5s` | потолок одной пробы `/ready`; `> 0` |
| `HTTP_QUIET_PATHS` | `QuietPaths` | `[]string` | `/health,/ready` | пути без Info-строки access-log и без server-спана |
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
| `HTTP_CORS_ORIGINS` | `Origins` | `[]string` | пусто (CORS off) | разрешённые Origin; `*` = любой |
| `HTTP_CORS_METHODS` | `Methods` | `[]string` | `GET,POST,PUT,PATCH,DELETE,OPTIONS` | `Access-Control-Allow-Methods` |
| `HTTP_CORS_HEADERS` | `Headers` | `[]string` | `Authorization,Content-Type,X-Request-ID` | `Access-Control-Allow-Headers` |
| `HTTP_CORS_EXPOSE_HEADERS` | `ExposeHeaders` | `[]string` | `X-Request-ID,Location,Retry-After,Idempotent-Replayed` | `Access-Control-Expose-Headers`: без него cross-origin JS не читает даже заголовки, которые ставит сам кит |
| `HTTP_CORS_MAX_AGE` | `MaxAge` | `int` | `600` | `Access-Control-Max-Age` (сек); `>= 0` |
| `HTTP_CORS_ALLOW_CREDENTIALS` | `AllowCredentials` | `bool` | `false` | `Access-Control-Allow-Credentials: true`; несовместим с `*` (ошибка `Validate`) |

## RateLimitConfig

Token-bucket на IP клиента (`golang.org/x/time/rate`). Превышение → `429
RATE_LIMITED` с `Retry-After: 1` и `application/problem+json`. Простаивающие bucket'ы
выметаются (5 минут без запросов), память ограничена активными клиентами.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_RATE_LIMIT_RPS` | `RPS` | `int` | `100` | запросов в секунду на IP; `0` — off |
| `HTTP_RATE_LIMIT_BURST` | `Burst` | `int` | `200` | ёмкость bucket; `0` → равна `RPS` |
| `HTTP_RATE_LIMIT_TRUST_PROXY` | `TrustProxy` | `bool` | `false` | IP берётся из **правого** значения последнего `X-Forwarded-For`, затем `X-Real-IP` |

`TrustProxy` рассчитан на топологию «ровно один доверенный прокси»:
nginx/traefik дописывают реальный IP в конец `X-Forwarded-For`
(`$proxy_add_x_forwarded_for`), а всё, что левее, прислал клиент. Клиент вправе
прислать и **свой** заголовок `X-Forwarded-For`, поэтому берётся правое значение
**последнего** из них (так же с `X-Real-IP`) — прокси, который добавляет
заголовок вместо замены, тоже безопасен. Значение должно парситься как IP,
иначе берётся `X-Real-IP`, иначе `RemoteAddr`. Ту же
логику отдаёт `httpkit.ClientIP(r, trustProxy)` — используйте её в антибрутфорсе
и аудите, чтобы ключ клиента был один на весь сервис.

## MaxBodyConfig

Оборачивает `r.Body` в `http.MaxBytesReader`. Превышение при чтении даёт
`*http.MaxBytesError` (`IsMaxBodyError`), ответ `413 BODY_TOO_LARGE` считается в
`http_max_body_rejections_total`.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_MAX_BODY_BYTES` | `MaxBytes` | `int64` | `1048576` (1 MiB) | лимит тела; `0` — off; `>= 0` |

Значение доезжает и до huma: `BindAPI` ставит `Operation.MaxBodyBytes` каждой
операции (иначе huma резал бы тело собственным дефолтом 1 MiB независимо от
`HTTP_MAX_BODY_BYTES`). Отдельной операции лимит меняется своим хуком
`WithHumaConfig` — он применяется после кита и выигрывает.

## SecurityHeadersConfig

Заголовки на каждый ответ; пустое значение — заголовок не ставится.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_HSTS` | `HSTS` | `string` | `max-age=31536000; includeSubDomains` | `Strict-Transport-Security` |
| `HTTP_CONTENT_TYPE_OPTIONS` | `ContentTypeOptions` | `string` | `nosniff` | `X-Content-Type-Options` |
| `HTTP_FRAME_OPTIONS` | `FrameOptions` | `string` | `DENY` | `X-Frame-Options` |
| `HTTP_REFERRER_POLICY` | `ReferrerPolicy` | `string` | `strict-origin-when-cross-origin` | `Referrer-Policy` |
| `HTTP_CSP` | `CSP` | `string` | пусто | `Content-Security-Policy` |

## IdempotencyConfig

Читается из ENV и применяется `Module`, когда в графе есть `IdempotencyStore`.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_IDEMPOTENCY_HEADER` | `Header` | `string` | `Idempotency-Key` | заголовок с ключом |
| `HTTP_IDEMPOTENCY_TTL` | `TTL` | `time.Duration` | `24h` | срок хранения ответа; `> 0` |
| `HTTP_IDEMPOTENCY_RESERVE_TTL` | `ReserveTTL` | `time.Duration` | `60s` | сколько ключ считается «выполняется», если процесс упал в середине запроса; `> 0` |
| `HTTP_IDEMPOTENCY_MAX_BODY_BYTES` | `MaxBodyBytes` | `int64` | `65536` | потолок буферизации запроса и ответа; больше — запрос идёт без идемпотентности; `> 0` |
| `HTTP_IDEMPOTENCY_STORE_TIMEOUT` | `StoreTimeout` | `time.Duration` | `2s` | потолок одной операции store; запись результата не зависит от отмены ctx клиента; `> 0` |
| `HTTP_IDEMPOTENCY_FAIL_CLOSED` | `FailClosed` | `bool` | `false` | `true` — недоступный store отвечает `503 IDEMPOTENCY_UNAVAILABLE`; `false` — запрос выполняется без дедупликации (fail-open) |

## Правила Validate

- `Addr` не пуст; таймауты `>= 0`; `ShutdownTimeout > 0`; `ReadyProbeTimeout > 0`.
- `APITitle` и `APIVersion` не пусты.
- `RateLimit.RPS`, `RateLimit.Burst`, `MaxBody.MaxBytes`, `CORS.MaxAge` `>= 0`.
- `Idempotency`: `Header` не пуст, `TTL`, `ReserveTTL`, `MaxBodyBytes`,
  `StoreTimeout` `> 0`.
- `CORS.AllowCredentials` несовместим с origin `*`.

Zero-value `Config` отвергается (`Addr` пуст) — сервер стартует только на
осознанно заданных значениях.

## Public API

Имена полей и envconfig-теги всех перечисленных структур — part of public API
(корневой [README](../../README.md) § Стабильность API).
