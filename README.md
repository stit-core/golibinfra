<div align="center">

# golibinfra

### Инфраструктурные Go-kit'ы с observability из коробки

Набор переиспользуемых Go-библиотек для сервисов экосистемы stit:
auth, БД, messaging, кэш, аудит, storage, AI-клиент. Каждый kit — `fx.Module`,
инструментирован OTel, подключается независимо. Импортируется как Go-модуль из
бизнес-репозиториев.

[![License](https://img.shields.io/badge/license-Apache--2.0-2563eb)](LICENSE)
[![Часть stit](https://img.shields.io/badge/stit-открытые_шаблоны-6366f1)](https://git.stit.tech/stit-core/stit-public)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8)](#использование-в-проекте)
[![Observability](https://img.shields.io/badge/observability-OTel_by_default-8b5cf6)](#observability)

</div>

```go
import "git.stit.tech/stit-core/golibinfra/pgkit"
import "git.stit.tech/stit-core/golibinfra/httpkit"
import "git.stit.tech/stit-core/golibinfra/healthkit"
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

Проверка: `go list -m -versions git.stit.tech/stit-core/golibinfra` показывает теги.

## Использование в проекте

```bash
go get git.stit.tech/stit-core/golibinfra@latest   # или @vX.Y.Z из `git tag`
go mod tidy
```

Модуль требует Go ≥ 1.26.6 (директива `go` в `go.mod`; при `GOTOOLCHAIN=auto`
нужный toolchain скачается сам).

## Принципы

- **Никакой продуктовой специфики.** Если файл знает про конкретные роли, домены,
  имена сервисов — он не здесь. Исключения по решению оператора — `hubkit` (SDK
  хаба stit) и `amocrmkit`; условия их выноса — в `.claude/plans/roadmap.md`.
- **Без скрытых зависимостей.** Каждый пакет подключается независимо; `Config`
  даёт caller, `NewConfig` валидирует и падает на старте.
- **fx-first.** Каждый пакет экспортирует `Module` для проводки.
- **Observability-by-default.** Все компоненты инструментированы OTel.
- **Безопасные дефолты.** Cookie `Secure`, обязательный `aud`, таймауты у каждого
  исходящего вызова, retry только идемпотентных запросов.

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
| [`pgkit`](pgkit/) | `*pgkit.DB` фасад, `Transactor` (`WithTx`/`WithTxOptions`, откат при panic), миграции с версией на источник (`Source`, `MigrateModule`, fx-группа `pgkit.migrations`), маппинг pg-ошибок, `DBProbe`; `ReadWriteDB` для read-replica (`PG_REPLICA_DSN`); `db_pool_*{role}` |
| [`storagekit`](storagekit/) | S3/object storage: `Storage` (стриминговый `Put`, идемпотентный `Delete`), `Presigner`, `URLBuilder`, in-memory адаптер для тестов, `minio`-подпакет; `storage_ops_*` |
| [`httpkit`](httpkit/) | huma/chi bootstrap; middleware: RequestID, OTel, Recovery, SecurityHeaders, CORS, RateLimit, MaxBody, AccessLog, Metrics, Idempotency, HumaAuth; `/health` + `/ready` по пробам `healthkit`; `APIError`, `MapErr`, `MapIs`, `MapAs` |
| [`grpckit`](grpckit/) | gRPC-сервер: `*grpc.Server` + интерсепторы (recovery/request-id/access-log/опц. `Authenticator`-порт), otel (StatsHandler), health + опц. reflection, сервисы через fx-group `grpc.services` |
| [`natskit`](natskit/) | Pub/sub поверх NATS/JetStream: `Bus`/`StreamBus`, `PublishJSON[T]`, `SubscribeJSON[T]` → `Subscription`, graceful drain, DLQ, deadline обработчика, backpressure, несколько шин через `Module(name)` |
| [`httpclient`](httpclient/) | HTTP-клиент: retry только идемпотентных запросов (`GetBody`, `Retry-After`), circuit breaker на хост, OTel-спаны, `X-Request-ID` из ctx, SSE-стримы |
| [`amocrmkit`](amocrmkit/) | Транспорт к amoCRM: REST v4 (Bearer) + amoJo Chat (HMAC-SHA1) + Drive + OAuth2; валидация subdomain/TLD, per-account rate-limit на каждую попытку, таксономия ошибок |
| [`anthropickit`](anthropickit/) | Клиент Anthropic (`anthropic-sdk-go`): `API` (`Complete`/`Stream`/`CountTokens`), нормализованный `Usage` + `Cost` (`USDMicros`, флаг известной цены), спаны и метрики вызовов; `testkit/anthropictest` |
| [`grpcclient`](grpcclient/) | gRPC-клиент: `*grpc.ClientConn` + otel (StatsHandler), access-log, keepalive, `PerRPCCredentials` (bearer-токен из env) |
| [`cachekit`](cachekit/) | Redis: `Cache` (`Get`/`Set`/`SetNX`/`GetDel`/теги), `GetJSON[T]`/`SetJSON[T]`, `IdempotencyStore` для `httpkit`, `MemoryCache` для тестов; `cache_commands_*` |
| [`healthkit`](healthkit/) | Тип health-пробы `Probe` и `healthkit.Provide` в fx-группу `health`; `/ready` в `httpkit` собирает пробы китов |
| [`flagkit`](flagkit/) | Feature flags из ENV (`FEATURE_FLAGS=foo=true,bar=baz`); `Store.Bool/String` |
| [`timerkit`](timerkit/) | Periodic jobs через fx-группу `timer.job`: валидация на старте, ожидание джобов на стопе, `Timeout`/`Jitter`/`RunOnStart`; `timer_job_*` |
| [`telemetry`](telemetry/) | Opt-in bootstrap OTel SDK: tracer/meter/logger providers + OTLP HTTP-экспортёры (`OTEL_EXPORTER_OTLP_ENDPOINT` — URL по спецификации); `log.LoggerProvider` для `logkit`; `Span` helper. Без него kit-инструментация no-op |
| [`logkit`](logkit/) | `Logger` поверх slog: JSON в stdout + OTel log-bridge, redact PII на уровне атрибутов (`WithRedact`), trace_id/request_id из ctx, `RegisterAttr`; `log_messages_total` |
| [`authnkit`](authnkit/) | `Hasher` (Argon2id, строгий PHC, `NeedsRehash`); HS256 JWT на go-jose (`SignAccess`/`SignRefresh`, `JWT_LEEWAY`); `TOTP` (`TOTP_SKEW`); RBAC `Registry` |
| [`oidckit`](oidckit/) | Authentik/OIDC BFF: `Verifier` (discovery, JWKS-кэш с троттлингом, iss/aud/exp/nonce, PKCE S256), роуты login/callback/me/logout, cookie-сессия HS256 (`Secure` по умолчанию), `Extractor` → `httpkit.AuthExtractor`, `StateStore` (Redis, атомарный `GetDel`); `oidc_*` |
| [`hubkit`](hubkit/) | SDK хаба stit: `sso` (verify hub-session двух контуров, обязательный `aud`, JWKS-кэш, `Identity` в ctx, гейты), `issuer` (ES256 + ротация kid, JWKS-handler, rotating-refresh с reuse-detection поверх `cachekit`), `client` (типизированный hub API поверх `httpclient`), `sync` (pull-синк проекций), `projection` (эталон схемы) |
| [`bizmetric`](bizmetric/) | Helper для бизнес-метрик: `Counter/Histogram/Gauge`, форсит конвенцию `biz_<domain>_<event>_*` (panic при нарушении) |
| [`outboxkit`](outboxkit/) | Transactional outbox: тик воркера в одной транзакции (`batch` — SKIP LOCKED между репликами, `ordered` — один активный воркер), публикация в JetStream с дедупом, poison→dead, партиции создаются на старте, retention без потери pending; `BacklogReporter`; `outbox_*{instance}`; несколько инстансов через `New` |
| [`auditkit`](auditkit/) | Audit-log: `Entry`/`Logger`, core+pgstore split, время — часы БД |
| [`bruteforcekit`](bruteforcekit/) | Brute-force guard: атомарный счётчик попыток по паре (subject, IP), `LockState`, core+pgstore split |
| [`clockkit`](clockkit/) | `Clock` + test-fakes (`Fixed`, `Tickable`) для детерминированных временных тестов |
| [`tzkit`](tzkit/) | Канон времени: `Zone` (`APP_TZ`, fail-fast), `Day` и `WallTime` с huma-схемой и pgtype-интеропом, границы суток в зоне приложения |
| [`ptrkit`](ptrkit/) | Generic-хелперы указателей: `Of` (адрес значения), `Ptr` (nil-on-zero), `Deref` (nil-safe) |
| [`mapkit`](mapkit/) | Декодеры `map[string]any`: `String/Int/Int64/Float/Strings/Map/IntPtr/BoolPtr`; nil-safe |
| [`idkit`](idkit/) | Внутренние id: `NewV7()` — UUID v7 строкой (time-sortable) |
| [`config`](config/) | Композиция ENV-источников (`Source`, `Load`) для App-конфига приложения |
| [`testkit`](testkit/) | testcontainers: `StartPostgres(t, WithMigrations(...))`, `StartNATS(t, WithJetStream())`, `StartRedis`; httptest helpers; per-kit subpackages |

Служебные пакеты `internal/otelkit` (scope и `Must` для OTel-инструментов),
`internal/requestid` (ctx-ключ request-id) и `internal/jwks` (JWKS-кэш с
троттлингом) наружу не экспортируются.

## Per-concern endpoints (multi-host)

Каждый кит читает свой эндпоинт из ENV — **разное значение = разный хост**. Это
«шов» для split-топологии: storage / Postgres / NATS / Redis можно
развести по отдельным серверам, **не трогая код**. Репликация / HA / failover —
дело потребителя (швы и скиллы — в `corp-infra-template` и
`project-infra-template-compose`).

| Env | Кит | Назначение |
|---|---|---|
| `PG_DSN` | `pgkit` | Postgres (primary); `PG_CONNECT_TIMEOUT` — таймаут подключения на старте |
| `PG_REPLICA_DSN` | `pgkit` | Postgres read-replica (опц.; `ReadWriteDB`) |
| `STORAGE_S3_ENDPOINT` | `storagekit` | S3 / MinIO object storage |
| `NATS_URL` | `natskit` | NATS / JetStream |
| `REDIS_URL` | `cachekit` | Redis (cache + idempotency store + refresh/state store) |
| `OIDC_ISSUER` | `oidckit` | OIDC-провайдер (Authentik): discovery, token endpoint, JWKS |
| `HUB_SSO_JWKS_URL` | `hubkit` | JWKS employee-backend для verify сессии сотрудника; `HUB_SSO_AUDIENCE` обязателен |
| `HUB_SSO_CUSTOMER_JWKS_URL` | `hubkit` | JWKS company-backend для verify сессии заказчика; пусто → контур выключен; `HUB_SSO_CUSTOMER_AUDIENCE` обязателен при включённом контуре |
| `HUB_BASE_URL` | `hubkit` | hub API (company-backend; employees — `HUB_EMPLOYEE_BASE_URL`), m2m-токен `HUB_SERVICE_TOKEN` |
| `ANTHROPIC_BASE_URL` | `anthropickit` | Anthropic API host; ключ — `ANTHROPIC_API_KEY` |
| `GRPC_ADDR` | `grpckit` | bind gRPC-сервера (HTTP/2; default `:9090`) |
| `GRPC_CLIENT_TARGET` | `grpcclient` | адрес downstream gRPC-сервиса (токен — `GRPC_CLIENT_TOKEN`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `telemetry` | OTLP HTTP-коллектор, полный URL (`http://collector:4318`) |

`outboxkit` **не** имеет своего эндпоинта — он co-located с pg-хостом: outbox
пишется в той же транзакции, что и доменные данные (атомарность), поэтому живёт
на том же Postgres, что `PG_DSN`.

## Observability

Киты инструментированы OTel: HTTP/gRPC/DB/bus/cache/storage/AI-вызовы эмитят
метрики и спаны через **глобальные** OTel-провайдеры (`internal/otelkit` поверх
`otel.Meter`/`otel.Tracer`), а `logkit` пишет структурный лог в stdout и, при
подключённом `telemetry`, в OTLP. Пока приложение не зарегистрирует
`MeterProvider`/`TracerProvider`, инструменты — **no-op** (записи молча
отбрасываются, kit не падает). Экспорт и бэкенд — ответственность
потребителя: либо готовый opt-in кит `telemetry` (OTLP HTTP-экспортёры всех
трёх сигналов, endpoint/Bearer из env), либо собственная проводка OTel SDK.

```go
fx.New(
  fx.Provide(telemetry.NewConfig, logkit.NewConfig, httpkit.NewConfig, pgkit.NewPoolConfig, /* ... */),
  telemetry.Module,   // OTLP-экспортёры; без него всё ниже — no-op
  logkit.Module,      // log_messages_total{level}, slog с trace_id в stdout + OTLP
  httpkit.Module,     // http_requests_total, http_request_duration_seconds, server-span
  pgkit.Module,       // db_pool_*{role}, pg traces
  natskit.Module(""), // nats_messages_*, nats_handle_duration_seconds, consumer-span
  outboxkit.Module,   // outbox_*{instance}
  cachekit.Module,    // cache_commands_total, cache_command_duration_seconds
  timerkit.Module,    // timer_job_runs_total, timer_job_duration_seconds
  healthkit.Provide(pgkit.DBProbe), healthkit.Provide(natskit.Probe), healthkit.Provide(cachekit.Probe),
)
```

Имена метрик — Prometheus-стиль `<kit_prefix>_<what>_<unit|total>`; label'ы —
конечные множества (route-pattern, хост, инстанс, результат), не пути и не id.

### Business-метрики (`biz_*`)

Свои метрики бизнес-уровня — через `bizmetric` (форсит конвенцию
`biz_<domain>_<event>_total|_seconds|<gauge>`, panic при нарушении):

```go
logins := bizmetric.Counter(meter, "auth", "login_attempts")  // → biz_auth_login_attempts_total
logins.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "ok")))
```

### Что эмитит каждый пакет

Точный перечень сигналов кита (метрики — имена, типы, labels; спаны) живёт в его
README § Observability — у сложных китов в `docs/observability.md`. Там же
помечено, что эмитится opt-in (например `outbox_lag_seconds` / `outbox_pending_count`
— только при подключённом `BacklogReporter`).

## Стабильность API

Публичные конфиг-структуры китов (их поля и envconfig-теги) — **part of public
API**: менять имена/типы полей или теги = breaking change. До `v1.0.0` breaking
идёт minor-релизом с маркером `!` и миграционными заметками в аннотации тега.
Полный перечень полей — в README каждого кита § Конфигурация (у сложных китов —
`docs/configuration.md`); `make docs-check` сверяет теги в коде с документацией.

## Версионирование

Семвер, теги `v0.x.y`. До `v1.0.0` API нестабилен — breaking changes допустимы в
minor. Релиз — skill `/release`: `make gate-clean` → аннотированный тег с release
notes (`Breaking` / `Изменения` / `Гейт`) → push. В проектах после релиза:
```bash
go get git.stit.tech/stit-core/golibinfra@vX.Y.Z && go mod tidy
```

## Гейт

```bash
make tools        # golangci-lint + govulncheck
make check        # fmt-check + vet + lint + test -short -race + build + tidy-check + govulncheck + docs-check
make test-full    # + testcontainers (Postgres/NATS/Redis/MinIO), нужен Docker
make gate-clean   # make check в чистом worktree от HEAD — перед тегом
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
