# httpkit — конфигурация

`Config` агрегирует все настройки сервера и вложенные конфиги middleware.
`NewConfig() (Config, error)` читает окружение через `envconfig.Process("", &c)`
— для приложений без App-pattern достаточно `fx.Provide(httpkit.NewConfig)`
рядом с `httpkit.Module`. `Module` сам `Config` **не** provide'ит (та же
asymmetry, что в `pgkit`/`telemetry`).

## Config

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_ADDR` | `Addr` | `string` | `:8080` | bind-адрес `*http.Server` |
| `HTTP_READ_TIMEOUT` | `ReadTimeout` | `time.Duration` | `15s` | `http.Server.ReadTimeout` |
| `HTTP_WRITE_TIMEOUT` | `WriteTimeout` | `time.Duration` | `30s` | `http.Server.WriteTimeout` |
| `HTTP_IDLE_TIMEOUT` | `IdleTimeout` | `time.Duration` | `60s` | `http.Server.IdleTimeout` |
| `HTTP_SHUTDOWN_TIMEOUT` | `ShutdownTimeout` | `time.Duration` | `15s` | дедлайн graceful-shutdown на `OnStop` |
| `HTTP_API_TITLE` | `APITitle` | `string` | `API` | title в OpenAPI |
| `HTTP_API_VERSION` | `APIVersion` | `string` | `0.1.0` | version в OpenAPI |
| — | `CORS` | `CORSConfig` | — | вложенный (см. ниже) |
| — | `RateLimit` | `RateLimitConfig` | — | вложенный |
| — | `MaxBody` | `MaxBodyConfig` | — | вложенный |
| — | `Security` | `SecurityHeadersConfig` | — | вложенный |

## CORSConfig

CORS-middleware — no-op при пустом `Origins`. Origin `*` разрешает любой
источник; иначе матчится точное совпадение.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_CORS_ORIGINS` | `Origins` | `[]string` | `""` (пусто → CORS off) | разрешённые Origin; `*` = любой |
| `HTTP_CORS_METHODS` | `Methods` | `[]string` | `GET,POST,PUT,PATCH,DELETE,OPTIONS` | `Access-Control-Allow-Methods` |
| `HTTP_CORS_HEADERS` | `Headers` | `[]string` | `Authorization,Content-Type,X-Request-ID` | `Access-Control-Allow-Headers` |
| `HTTP_CORS_MAX_AGE` | `MaxAge` | `int` | `600` | `Access-Control-Max-Age` (сек) |

## RateLimitConfig

Per-IP token-bucket (`golang.org/x/time/rate`). No-op при `RPS == 0`.
Превышение → `429 Too Many Requests`.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_RATE_LIMIT_RPS` | `RPS` | `int` | `100` | запросов в секунду на IP (`0` → off) |
| `HTTP_RATE_LIMIT_BURST` | `Burst` | `int` | `200` | размер всплеска (ёмкость bucket) |

## MaxBodyConfig

Оборачивает `r.Body` в `http.MaxBytesReader`. No-op при `MaxBytes == 0`.
Превышение лимита при чтении body даёт `413` + инкремент
`http_max_body_rejections_total`.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_MAX_BODY_BYTES` | `MaxBytes` | `int64` | `1048576` (1 MiB) | лимит размера тела (`0` → off) |

## SecurityHeadersConfig

Выставляет security-заголовки на каждый ответ. Пустое значение поля → заголовок
не ставится. Не все поля имеют env-тег — `ContentTypeOptions`, `FrameOptions`,
`ReferrerPolicy` берут только default.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTP_HSTS` | `HSTS` | `string` | `max-age=31536000; includeSubDomains` | `Strict-Transport-Security` |
| — | `ContentTypeOptions` | `string` | `nosniff` | `X-Content-Type-Options` |
| — | `FrameOptions` | `string` | `DENY` | `X-Frame-Options` |
| — | `ReferrerPolicy` | `string` | `strict-origin-when-cross-origin` | `Referrer-Policy` |
| `HTTP_CSP` | `CSP` | `string` | `""` (пусто → заголовок не ставится) | `Content-Security-Policy` |

## IdempotencyConfig

Конфиг middleware дедупликации write-запросов. В fx-флоу `Module` собирает его с
фиксированными значениями (`Idempotency-Key` / `24h` / `65536`) и **не** читает
из ENV — env-тегов у полей нет, только `default`-теги. Кастомизация — через
прямой вызов `Idempotency(cfg, store)` мимо `Module`.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| — | `Header` | `string` | `Idempotency-Key` | заголовок с ключом идемпотентности |
| — | `TTL` | `time.Duration` | `24h` | срок жизни закэшированного ответа (для `MemoryStore`) |
| — | `MaxBodyBytes` | `int` | `65536` | бюджет на буферизацию ответа |

## Public API

Имена полей и envconfig-теги `Config`, `SecurityHeadersConfig`,
`IdempotencyConfig` (и вложенных `CORSConfig`/`RateLimitConfig`/`MaxBodyConfig`)
— **part of public API**. Переименование поля, смена тега или типа — breaking
change и идёт мажорным апгрейдом. См. корневой
[README](../../README.md) § Стабильность API.
