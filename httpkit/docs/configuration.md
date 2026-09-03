# httpkit — конфигурация

`Config` агрегирует настройки сервера и вложенные конфиги middleware.
`NewConfig() (Config, error)` читает окружение через envconfig и вызывает
`Config.Validate()`; `Module` валидирует ещё раз для конфигов, собранных вручную.
Ошибки валидации — `httpkit.ErrConfig`.

## Config

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_ADDR` | `Addr` | `string` | `:8080` | bind-адрес; `:0` — свободный порт (`Server.Addr()`) |
| `HTTP_READ_TIMEOUT` | `ReadTimeout` | `time.Duration` | `15s` | `http.Server.ReadTimeout` |
| `HTTP_READ_HEADER_TIMEOUT` | `ReadHeaderTimeout` | `time.Duration` | `10s` | `http.Server.ReadHeaderTimeout` (slowloris) |
| `HTTP_WRITE_TIMEOUT` | `WriteTimeout` | `time.Duration` | `30s` | `http.Server.WriteTimeout`; для SSE ставьте `0` |
| `HTTP_IDLE_TIMEOUT` | `IdleTimeout` | `time.Duration` | `60s` | `http.Server.IdleTimeout` |
| `HTTP_SHUTDOWN_TIMEOUT` | `ShutdownTimeout` | `time.Duration` | `15s` | дедлайн graceful-shutdown на `OnStop`; `> 0` |
| `HTTP_API_TITLE` | `APITitle` | `string` | `API` | title в OpenAPI |
| `HTTP_API_VERSION` | `APIVersion` | `string` | `0.1.0` | version в OpenAPI |
| — | `CORS` | `CORSConfig` | — | вложенный |
| — | `RateLimit` | `RateLimitConfig` | — | вложенный |
| — | `MaxBody` | `MaxBodyConfig` | — | вложенный |
| — | `Security` | `SecurityHeadersConfig` | — | вложенный |
| — | `Idempotency` | `IdempotencyConfig` | — | вложенный |

## CORSConfig

No-op при пустом `Origins`. `*` разрешает любой origin и отвечает `*` в
`Access-Control-Allow-Origin`; конкретный origin эхается с `Vary: Origin`.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_CORS_ORIGINS` | `Origins` | `[]string` | пусто (CORS off) | разрешённые Origin; `*` = любой |
| `HTTP_CORS_METHODS` | `Methods` | `[]string` | `GET,POST,PUT,PATCH,DELETE,OPTIONS` | `Access-Control-Allow-Methods` |
| `HTTP_CORS_HEADERS` | `Headers` | `[]string` | `Authorization,Content-Type,X-Request-ID` | `Access-Control-Allow-Headers` |
| `HTTP_CORS_MAX_AGE` | `MaxAge` | `int` | `600` | `Access-Control-Max-Age` (сек) |
| `HTTP_CORS_ALLOW_CREDENTIALS` | `AllowCredentials` | `bool` | `false` | `Access-Control-Allow-Credentials: true`; несовместим с `*` (ошибка `Validate`) |

## RateLimitConfig

Token-bucket на IP клиента (`golang.org/x/time/rate`). Превышение → `429` с
`Retry-After: 1` и `application/problem+json`. Простаивающие bucket'ы
выметаются (5 минут без запросов), память ограничена активными клиентами.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_RATE_LIMIT_RPS` | `RPS` | `int` | `100` | запросов в секунду на IP; `0` — off |
| `HTTP_RATE_LIMIT_BURST` | `Burst` | `int` | `200` | ёмкость bucket; `0` → равна `RPS` |
| `HTTP_RATE_LIMIT_TRUST_PROXY` | `TrustProxy` | `bool` | `false` | IP из первого значения `X-Forwarded-For`, затем `X-Real-IP`; включайте только за собственным reverse-proxy, иначе все клиенты за прокси делят один bucket, а без прокси заголовок подделывается |

## MaxBodyConfig

Оборачивает `r.Body` в `http.MaxBytesReader`. Превышение при чтении даёт
`*http.MaxBytesError` (`IsMaxBodyError`), ответ `413` считается в
`http_max_body_rejections_total`.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_MAX_BODY_BYTES` | `MaxBytes` | `int64` | `1048576` (1 MiB) | лимит тела; `0` — off |

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
| `HTTP_IDEMPOTENCY_TTL` | `TTL` | `time.Duration` | `24h` | срок хранения ответа |
| `HTTP_IDEMPOTENCY_RESERVE_TTL` | `ReserveTTL` | `time.Duration` | `60s` | сколько ключ считается «выполняется», если handler не завершился (crash) |
| `HTTP_IDEMPOTENCY_MAX_BODY_BYTES` | `MaxBodyBytes` | `int64` | `65536` | потолок буферизации запроса и ответа; больше — запрос идёт без идемпотентности |

## Правила Validate

- `Addr` не пуст; таймауты `>= 0`; `ShutdownTimeout > 0`.
- `RateLimit.RPS`, `RateLimit.Burst`, `MaxBody.MaxBytes` `>= 0`.
- `Idempotency`: `Header` не пуст, `TTL`, `ReserveTTL`, `MaxBodyBytes` `> 0`.
- `CORS.AllowCredentials` несовместим с origin `*`.

## Public API

Имена полей и envconfig-теги всех перечисленных структур — part of public API
(корневой [README](../../README.md) § Стабильность API).
