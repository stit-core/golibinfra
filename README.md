<div align="center">

# golibinfra

### Инфраструктурные Go-kit'ы с observability из коробки

Набор переиспользуемых Go-библиотек для сервисов экосистемы stit:
auth, БД, messaging, кэш, аудит, storage, AI-клиенты. Каждый kit — `fx.Module`,
инструментирован OTel, подключается независимо. Импортируется как Go-модуль из
бизнес-репозиториев.

[![License](https://img.shields.io/badge/license-Apache--2.0-2563eb)](LICENSE)
[![Часть stit](https://img.shields.io/badge/stit-открытые_шаблоны-6366f1)](https://git.stit.tech/stit-core/stit-public)
[![Go](https://img.shields.io/badge/Go-module-00ADD8)](#использование-в-проекте)
[![Observability](https://img.shields.io/badge/observability-OTel_by_default-8b5cf6)](#observability)
[![Kits](https://img.shields.io/badge/kits-30-0ea5e9)](#пакеты)

</div>

```go
import "git.stit.tech/stit-core/golibinfra/pgkit"
import "git.stit.tech/stit-core/golibinfra/httpkit"
import "git.stit.tech/stit-core/golibinfra/config"
import "git.stit.tech/stit-core/golibinfra/testkit"
```

## Где это в экосистеме

`golibinfra` — общая Go-lib экосистемы stit (5 форкаемых шаблонов + 2 общие
библиотеки). Её импортирует
[`go-backend-template`](https://git.stit.tech/stit-core/go-backend-template)
(форк `<org>/backend`). Карта всех репо — на
[витрине stit](https://git.stit.tech/stit-core/stit-public).

## Доступ к репозиторию

Репо лежит на собственной Gitea (`git.stit.tech`), не на публичном Go-proxy.
Чтобы `go get` / `go mod tidy` мог его подтянуть, настройте `GOPRIVATE` (чтобы Go
не шёл через `proxy.golang.org`/`sum.golang.org`); для авторизованного доступа —
git-аутентификацию SSH-ключом или токеном.

```bash
# 1. SSH-ключ в Gitea (если нет): ssh-keygen -t ed25519 -C "you@stit.tech"
#    залить ~/.ssh/id_ed25519.pub в https://git.stit.tech/-/user/settings/keys
ssh -T git@git.stit.tech                                   # проверить

# 2. GOPRIVATE: git.stit.tech — приватный
go env -w GOPRIVATE='git.stit.tech/*'

# 3. SSH вместо HTTPS для модулей
git config --global url."git@git.stit.tech:".insteadOf "https://git.stit.tech/"
```

**Альтернатива (CI без ssh-agent):** HTTPS + токен в `~/.netrc`:
```bash
# Gitea → Settings → Applications → Generate Token (scope: read:repository)
printf 'machine git.stit.tech\nlogin %s\npassword %s\n' "$USER" "$GITEA_TOKEN" >> ~/.netrc
chmod 600 ~/.netrc
```

Проверка: `go get git.stit.tech/stit-core/golibinfra@v0.24.0` скачивает без ошибок.

## Использование в проекте

```
require git.stit.tech/stit-core/golibinfra v0.24.0
```

`go mod tidy` подтянет.

## Принципы

- **Никакой продуктовой специфики.** Если файл знает про конкретные роли, домены,
  имена сервисов — он не здесь.
- **Без скрытых зависимостей.** Каждый пакет подключается независимо.
- **fx-first.** Каждый пакет экспортирует `Module` для проводки.
- **Observability-by-default.** Все компоненты инструментированы OTel.

## Пакеты

У каждого кита — собственный `README.md` рядом с его кодом: назначение, быстрый
старт, конфигурация, observability, API, инварианты. Это и есть единый источник
правды по киту. У сложных китов (`amocrmkit`, `anthropickit`, `authnkit`,
`grpckit`, `httpkit`, `natskit`, `outboxkit`, `pgkit`) сверх README есть каталог
`docs/` (`architecture.md`, `configuration.md`, `observability.md`, `usage.md`,
`testing.md`). Таблица ниже — тонкий индекс: ответственность + ссылка
`[<kit>](<kit>/)` открывает README кита.

| Пакет | Ответственность |
|---|---|
| [`pgkit`](pgkit/) | `*pgkit.DB` фасад, Transactor, migrate-runner, маппинг pg-ошибок, `DBProbe` health-probe; `ReadWriteDB` для read-replica (`PG_REPLICA_DSN`) |
| [`storagekit`](storagekit/) | S3/object storage: `Storage` (`Put`/`Delete`), `URLBuilder`, in-memory адаптер для тестов, `minio`-подпакет; `Config` + `NewKey` (content-addressed) |
| [`httpkit`](httpkit/) | Huma bootstrap; middleware: RequestID, Recovery, AccessLog, OTel, CORS, RateLimit, MaxBody, Metrics, HumaAuth, **SecurityHeaders**, **Idempotency**; `APIError`, `MapErr`, `MapSentinel` |
| [`grpckit`](grpckit/) | gRPC-сервер: `*grpc.Server` + интерсепторы (recovery/request-id/access-log/опц. `Authenticator`-порт), otel (StatsHandler), health + опц. reflection, сервисы через fx-group `grpc.services` |
| [`natskit`](natskit/) | Pub/sub поверх NATS: `Bus`, `PublishJSON[T]`, `SubscribeJSON[T]`, fx.Module; panic recovery + backpressure (`NATS_MAX_IN_FLIGHT`) |
| [`httpclient`](httpclient/) | HTTP-клиент с retry, circuit breaker (`sony/gobreaker/v2`), OTel и автопередачей `X-Request-ID` |
| [`amocrmkit`](amocrmkit/) | Транспорт к amoCRM: REST v4 (Bearer) + amoJo Chat (HMAC-SHA1) + Drive + OAuth2; per-account rate-limit (7 rps), таксономия ошибок (`ErrAuth/ErrRateLimited/…`), `TokenSource`; обёртка над `httpclient`/`clockkit`/`logkit` |
| [`anthropickit`](anthropickit/) | Клиент Anthropic (`anthropic-sdk-go`): `API` (`Complete`/`Stream`/`CountTokens`), нормализованный `Usage` + `Cost` (`USDMicros`, прайс-таблица), Managed Agents через `Raw()`; `testkit/anthropictest` |
| [`openaikit`](openaikit/) | Клиент OpenAI (`openai-go/v3`): `API` (Responses + Chat + Embeddings + Images + Audio STT/TTS + Moderation), нормализованный `Usage` + `Cost` (**`USDMicros`**, прайс-таблица), экзотика через `Raw()`; `testkit/openaitest` |
| [`googleaikit`](googleaikit/) | Клиент Google Gemini (`google.golang.org/genai`, dual-backend AI-Studio/Vertex): `API` (GenerateContent + Stream + Embeddings + Images + Video + Speech), нормализованный `Usage` + `Cost` (**`USDMicros`**, прайс-таблица), `Raw()`; `testkit/googletest` |
| [`yandexaikit`](yandexaikit/) | Клиент Yandex FM (raw REST, IAM-JWT + async-операции): `API` (Completion sync/async + Stream + Embeddings + YandexART), нормализованный `Usage` + `Cost` (**`RUBMicros`**, sync/async прайс-таблицы), `Raw()`; `testkit/yandextest` |
| [`sberaikit`](sberaikit/) | Клиент Sber GigaChat (raw HTTP, OAuth2 + Russian-Trusted CA): `API` (Chat + Stream + Embeddings + Files), нормализованный `Usage` + `Cost` (**`RUBMicros`**, `CurrencyRUB`, прайс-таблица), `Raw()`; `testkit/sbertest` |
| [`grpcclient`](grpcclient/) | gRPC-клиент: `*grpc.ClientConn` + otel (StatsHandler), access-log, keepalive, `PerRPCCredentials` (bearer-токен из env) |
| [`webpushkit`](webpushkit/) | Web Push доставка: VAPID (RFC 8292) + aes128gcm-шифрование payload (RFC 8291/8188) + POST на endpoint (RFC 8030); `Sender.Send`, `Subscription`, `GenerateVAPIDKeys`, `ErrSubscriptionGone` (404/410 → чистка подписки). Подписки хранит потребитель |
| [`cachekit`](cachekit/) | Redis-обёртка: `Cache`, `GetJSON[T]`/`SetJSON[T]`, `InvalidateByTag`; `IdempotencyStore` для `httpkit` |
| [`flagkit`](flagkit/) | Feature flags из ENV (`FEATURE_FLAGS=foo=true,bar=baz`); `Store.Bool/String` |
| [`timerkit`](timerkit/) | Periodic jobs через fx group: регистрация задач с интервалом, lifecycle-aware |
| [`telemetry`](telemetry/) | Opt-in bootstrap OTel SDK: tracer/meter/logger providers + OTLP HTTP-экспортёры (endpoint/Bearer из env); resource-атрибуты `service.*`, опц. `tenant`; runtime-инструментация. Без него kit-инструментация no-op |
| [`logkit`](logkit/) | `Logger` поверх slog; JSON-stdout (`LOG_STDOUT`) или произвольный writer (`NewWithWriter`); `log_messages_total`; trace_id correlation; `RegisterAttr`; `NewWithRedact` (PII-фильтры) |
| [`authnkit`](authnkit/) | `Hasher` (Argon2id); `JWTConfig` + sign/verify; `TOTPGenerator` (RFC 6238); `Permission`/`RolePermissions`/`Has` для RBAC |
| [`oidckit`](oidckit/) | Authentik/OIDC BFF: `Verifier` (discovery, JWKS lazy-refresh, verify iss/aud/exp/nonce, PKCE S256), huma-роуты login/callback/me/logout, cookie-сессия HS256 (`authnkit`), `Extractor` → `httpkit.AuthExtractor`, `StateStore` (Redis/in-memory), `Hooks` (SessionClaims/OnLogin) |
| [`hubkit`](hubkit/) | Клиент центрального хаба stit: `sso` (verify hub-session JWT ES256/JWKS двух контуров — сотрудник `stit_session`/`id.stit.tech` и заказчик `stit_customer`/`api.hub.stit.tech`, приоритет сотрудника, `Identity{Kind,CompanyID,AccountRole,Products}` в ctx + `AuthExtractor`, гейты `RequireEmployee`/`RequireCustomer`), `issuer` (выпуск session-JWT: ES256 + ротация kid, JWKS-handler, cookie `Domain=.stit.tech`/SameSite=Lax + host-only refresh на своём `Path`, rotating-refresh поверх Redis + tombstone), `client` (типизир. hub API поверх `httpclient`: companies/projects/employees/visibility/sync, m2m Bearer, `ErrNotFound`/`ErrConflict`/`ErrUnavailable`), `sync` (pull-синк проекций: цикл `after=<cursor>`, порты `ProjectionStore`/`CursorStore`, `hub_sync_*`-метрики), `projection` (эталон схемы проекций: миграции + sqlc) |
| [`bizmetric`](bizmetric/) | Helper для бизнес-метрик: `Counter/Histogram/Gauge`, форсит конвенцию `biz_<domain>_<event>_*` (panic при нарушении) |
| [`outboxkit`](outboxkit/) | Transactional outbox: Worker публикует в JetStream (`MarkPublished` батчем после PubAck; `OUTBOX_PUBLISH_MODE` batch\|ordered), poison→dead после `OUTBOX_MAX_ATTEMPTS`, retention партиций `OUTBOX_RETENTION_MONTHS`, `BacklogReporter` для `outbox_lag_seconds`/`outbox_pending_count`. `Module` — один инстанс (таблица `outbox`); `outboxkit.New` + `pgstore.NewStoreFor(db, table)` + `NewStreamTransportFor(sb, namespace)` — несколько (таблица на инстанс, namespaced Msg-Id) |
| [`auditkit`](auditkit/) | Audit-log: `Entry`/`Logger`, core+pgstore split |
| [`bruteforcekit`](bruteforcekit/) | Brute-force guard: счётчик попыток по паре (subject, IP), `LockState`, core+pgstore split |
| [`clockkit`](clockkit/) | `Clock` + test-fakes (`Fixed`, `Tickable`) для детерминированных временных тестов |
| [`tzkit`](tzkit/) | Канон времени: `Zone` (`APP_TZ`, fail-fast), `Day` (`DATE`/`YYYY-MM-DD`) и `WallTime` (`TIME`/`HH:MM`) с huma-схемой и pgtype-интеропом, границы суток (`DayOf`/`StartOfDay`/`Span`) в зоне приложения; tzdata вшита в бинарь |
| [`ptrkit`](ptrkit/) | Generic-хелперы указателей: `Ptr` (nil-on-zero), `Deref` (nil-safe); без I/O |
| [`mapkit`](mapkit/) | Декодеры `map[string]any`: `String/Int/Int64/Float/Strings/Map/IntPtr/BoolPtr`; nil-safe, без I/O |
| [`idkit`](idkit/) | Внутренние id: `NewV7()` — UUID v7 строкой (time-sortable, PK-локальность); без I/O |
| [`config`](config/) | Загрузчик конфигурации из env, провайдит `[]Source` для приложения |
| [`testkit`](testkit/) | testcontainers: `StartPostgres`, `StartNATS`, `StartRedis`; httptest helpers; per-kit subpackages |

## Per-concern endpoints (multi-host)

Каждый кит читает свой эндпоинт из ENV — **разное значение = разный хост**. Это
«шов» для split-топологии: storage / Postgres / NATS / Redis можно
развести по отдельным серверам, **не трогая код**. Репликация / HA / failover —
дело потребителя (швы и скиллы — в `corp-infra-template` и
`project-infra-template-compose`).

| Env | Кит | Назначение |
|---|---|---|
| `PG_DSN` | `pgkit` | Postgres (primary) |
| `PG_REPLICA_DSN` | `pgkit` | Postgres read-replica (опц.; `ReadWriteDB`) |
| `STORAGE_S3_ENDPOINT` | `storagekit` | S3 / MinIO object storage |
| `NATS_URL` | `natskit` | NATS / JetStream |
| `REDIS_URL` | `cachekit` | Redis (cache + idempotency store) |
| `OIDC_ISSUER` | `oidckit` | OIDC-провайдер (Authentik): discovery, token endpoint, JWKS |
| `HUB_SSO_JWKS_URL` | `hubkit` | JWKS employee-backend (`id.stit.tech`) для verify сессии сотрудника |
| `HUB_SSO_CUSTOMER_JWKS_URL` | `hubkit` | JWKS company-backend (`api.hub.stit.tech`) для verify сессии заказчика; пусто → контур выключен |
| `HUB_BASE_URL` | `hubkit` | hub API (company-backend `api.hub.stit.tech`; employees — `HUB_EMPLOYEE_BASE_URL`), m2m-токен `HUB_SERVICE_TOKEN` |
| `ANTHROPIC_BASE_URL` | `anthropickit` | Anthropic API host (default `api.anthropic.com`); ключ — `ANTHROPIC_API_KEY` |
| `OPENAI_BASE_URL` | `openaikit` | OpenAI API host (default `api.openai.com/v1`); ключ — `OPENAI_API_KEY` |
| `GOOGLE_BASE_URL` | `googleaikit` | Gemini API host (бэкенд — `GOOGLE_BACKEND` aistudio\|vertex); ключ — `GOOGLE_API_KEY` (AI-Studio) или ADC (Vertex) |
| `YANDEX_BASE_URL` | `yandexaikit` | Yandex FM host (default `llm.api.cloud.yandex.net`); auth — `YANDEX_API_KEY` / service-account-JWT |
| `SBER_BASE_URL` | `sberaikit` | GigaChat host (OAuth via `SBER_OAUTH_URL`); ключ — `SBER_AUTH_KEY` |
| `GRPC_ADDR` | `grpckit` | bind gRPC-сервера (HTTP/2; default `:9090`) |
| `GRPC_CLIENT_TARGET` | `grpcclient` | адрес downstream gRPC-сервиса (токен — `GRPC_CLIENT_TOKEN`) |
| `WEBPUSH_VAPID_PRIVATE_KEY` | `webpushkit` | VAPID private (raw ES256); public — `WEBPUSH_VAPID_PUBLIC_KEY`, контакт — `WEBPUSH_SUBJECT` |

`outboxkit` **не** имеет своего эндпоинта — он co-located с pg-хостом: outbox
пишется в той же транзакции, что и доменные данные (атомарность), поэтому живёт
на том же Postgres, что `PG_DSN`.

## Observability

Киты инструментированы OTel: HTTP/gRPC/DB/bus/AI-вызовы эмитят метрики и спаны
через **глобальные** OTel-провайдеры (`otel.Meter`/`otel.Tracer`), а `logkit`
пишет структурный лог в stdout. Пока приложение не зарегистрирует
`MeterProvider`/`TracerProvider`, инструменты — **no-op** (записи молча
отбрасываются, kit не падает). Экспорт и бэкенд — ответственность
потребителя: либо готовый opt-in кит `telemetry` (OTLP HTTP-экспортёры всех
трёх сигналов, endpoint/Bearer из env), либо собственная проводка OTel SDK.

Подключение fx-модулей golibinfra даёт RED + DB-pool + bus + logs «из коробки» на
уровне инструментов:

```go
fx.New(
  logkit.Module,      // log_messages_total{level}, slog с trace_id в stdout
  httpkit.Module,     // http_requests_total, http_request_duration_seconds
  pgkit.Module,       // db_pool_* (acquired/idle/max/wait), pg traces
  natskit.Module,     // nats_messages_*, nats_handle_duration_seconds
  outboxkit.Module,   // outbox_events_*, outbox_batch_size, outbox_publish_duration_seconds
  authnkit.Module,    // jwt_verifications_total, password_*_duration_seconds
  ...
)
```

### Business-метрики (`biz_*`)

Свои метрики бизнес-уровня — через `bizmetric` (форсит конвенцию
`biz_<domain>_<event>_total|_seconds|<gauge>`, panic при нарушении):

```go
logins := bizmetric.Counter(meter, "auth", "login_attempts")  // → biz_auth_login_attempts_total
logins.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "ok")))
```

Единый префикс `biz_` держит конвенцию имён бизнес-метрик; не плодите их в обход
`bizmetric.Counter/Histogram/Gauge`.

### Что эмитит каждый пакет

Точный перечень сигналов кита (метрики — имена, типы, labels; спаны) живёт в его
README § Observability — у сложных китов в `docs/observability.md`. Там же
помечено, что эмитится opt-in (например `outbox_lag_seconds` / `outbox_pending_count`
— только при подключённом `BacklogReporter`).

## Стабильность API

Публичные конфиг-структуры китов (их поля и envconfig-теги) — **part of public
API**: менять имена/типы полей или теги = breaking change, идёт мажорным
апгрейдом. Полный перечень полей с пометкой стабильности — в README каждого кита
§ Конфигурация (у сложных китов — `docs/configuration.md`).

## Версионирование

Семвер, теги `v0.x.y`. До `v1.0.0` API нестабилен — breaking changes допустимы в
minor. Релиз — skill `/release` (`git tag vX.Y.Z` + push). В проектах после релиза:
```bash
go get git.stit.tech/stit-core/golibinfra@v0.24.0 && go mod tidy
```

## Локальная разработка с правкой golibinfra

Правите `golibinfra` параллельно с продуктом — временный `replace` в `go.mod`
продукта (**не коммитить**, перед мержем убрать и поднять версию через `go get`):
```
replace git.stit.tech/stit-core/golibinfra => ../golibinfra
```

## Часть экосистемы stit

Это одна из открытых библиотек stit (Apache-2.0). Карта всех шаблонов и точка
входа — **[витрина stit](https://git.stit.tech/stit-core/stit-public)**.
Нужна команда, стандарт или поддержка —
**[наш подход и контакты (vision)](https://git.stit.tech/stit/vision)**.

## Лицензия

**Apache-2.0** — см. [`LICENSE`](LICENSE).
