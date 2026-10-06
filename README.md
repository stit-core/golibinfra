<div align="center">

# golibinfra

### Инфраструктурные Go-kit'ы с observability из коробки

Набор переиспользуемых Go-библиотек для сервисов экосистемы stit:
auth, БД, messaging, кэш, аудит, storage, AI-клиент. Каждый kit подключается
независимо: кит с конфигом или ресурсом отдаёт `fx`-модуль, инструментация — OTel
по умолчанию. Импортируется как Go-модуль из бизнес-репозиториев.

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

`golibinfra` — общая Go-lib экосистемы stit (7 форкаемых шаблонов + 2 общие
библиотеки: эта и [`pylibinfra`](https://git.stit.tech/stit-core/pylibinfra) для
Python). Её импортирует
[`go-backend-template`](https://git.stit.tech/stit-core/go-backend-template)
(форк `<org>/backend`), а также бэкенды `ai-gateway-template` и
`amocrm-gateway-template`. Библиотека не форкается: потребители тянут модуль по
тегу, а выкатка их образов идёт из `<org>/infra`. Карта репо и операционный флоу —
[`docs/reference/ecosystem.md`](https://git.stit.tech/stit-core/project-infra-template-compose/src/branch/main/docs/reference/ecosystem.md)
в `project-infra-template-compose`; витрина всех репо —
[stit-public](https://git.stit.tech/stit-core/stit-public).

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
  имена сервисов — он не здесь. Исключения — `hubkit` (SDK хаба stit) и
  `amocrmkit`; их выносят в отдельные модули, когда появляется второй потребитель
  вне stit-core или конфликт версий зависимостей у потребителя.
- **Без скрытых зависимостей.** Каждый пакет подключается независимо; `Config`
  даёт caller, `NewConfig` валидирует и падает на старте.
- **fx-first.** Кит с конфигом или ресурсом экспортирует `Module` для проводки (у
  хранилищ `auditkit` и `bruteforcekit` — в сабпакете `pgstore`); хелперы без
  состояния (`ptrkit`, `mapkit`, `idkit`, `bizmetric`) и `healthkit` (`Provide`)
  модуля не имеют.
- **Observability-by-default.** Все компоненты с I/O инструментированы OTel.
- **Безопасные дефолты.** Cookie `Secure`, обязательный `aud` у SSO (`hubkit/sso`,
  `oidckit`), таймауты у каждого исходящего вызова, retry только идемпотентных запросов.

## Пакеты

У каждого кита — собственный `README.md` рядом с его кодом: назначение, быстрый
старт, конфигурация, observability, API, инварианты. Это и есть единый источник
правды по киту. У сложных китов (`amocrmkit`, `anthropickit`, `authnkit`,
`grpckit`, `httpkit`, `hubkit`, `natskit`, `outboxkit`, `pgkit`) сверх README
есть каталог `docs/` (`architecture.md`, `configuration.md`, `observability.md`,
`usage.md`, `testing.md`). Таблица ниже — тонкий индекс: ответственность + ссылка
`[<kit>](<kit>/)` открывает README кита.

| Пакет | Ответственность |
|---|---|
| [`pgkit`](pgkit/) | `*pgkit.DB` фасад, `Transactor` (`WithTx`/`WithTxOptions`, откат при panic), миграции с версией на источник (`Source`, `MigrateModule`, fx-группа `pgkit.migrations`, маркер порядка `Migrated`, `PG_MIGRATE_TIMEOUT`), маппинг pg-ошибок, `Probe`/`ReplicaProbe`; `ReadWriteDB` для read-replica (`PG_REPLICA_DSN`, опциональная); `pg_pool_*{role}` |
| [`storagekit`](storagekit/) | S3/object storage: `Storage` (`Put`/`Get`/`Stat`/`Delete`), `Presigner` (`PresignGet`/`PresignPut`), `URLBuilder` (path/virtual), `NewKey`, in-memory адаптер и контрактный набор `testkit/storagetest`, `minio`-подпакет с пробой и проверкой бакета на старте; `storage_ops_*` |
| [`httpkit`](httpkit/) | huma/chi bootstrap; HTTP стартует после всех `fx.Module` и гасится первым; middleware: RequestID, ClientIP, OTel, AccessLog, Metrics, Recovery, SecurityHeaders, CORS, RateLimit, MaxBody, Idempotency, HumaAuth (fail-closed: `Security` без `AuthExtractor` — ошибка старта; при схеме в графе операция — с `Security` или `httpkit.Public`, маршрут мимо huma — в `httpkit.PublicRoutes`, дубль METHOD+path и маршрут мимо huma на пути операции — ошибка старта); `/health` + `/ready` по пробам `healthkit` (параллельно, `HTTP_READY_PROBE_TIMEOUT`; panic пробы — `down`); остановка — `/ready` 503 на паузе `HTTP_SHUTDOWN_DRAIN_DELAY`, на дедлайне `HTTP_SHUTDOWN_TIMEOUT` ctx хендлеров отменяется и соединения закрываются, треть бюджета `fx.StopTimeout` — хукам после HTTP; подкоманда проверки здоровья образа без shell — `Healthcheck`; `/docs` и `/openapi.*` — только с `HTTP_DOCS_ENABLED=true` (по умолчанию выключены), без `$schema` и `Link` в ответах; контракт ошибок `APIError` (problem+json с `code`, `request_id`, `errors[]`; 5xx без причины, 429 с `Retry-After`, 404/405 маршрутизатора), `ErrorMap` доменных ошибок (`Map`/`Declare`/`Unmapped`, коды в OpenAPI), IP клиента в ctx (`ClientIPFrom`, `HTTP_TRUST_PROXY_HOPS`; ключ лимитера и `client.address` — он же, IPv6 по `/64`); точки расширения `WithMiddleware`/`WithHumaConfig`/`WithSecuritySchemes`, в графе — `httpkit.Middlewares` и `httpkit.HumaConfigs` (порядок слайса), fx-группа схем безопасности |
| [`grpckit`](grpckit/) | gRPC-сервер: `*grpc.Server` + интерсепторы (request-id → access-log → recovery → опц. `Authenticator`-порт), otel (StatsHandler), health по пробам группы `healthkit.probes` (`GRPC_HEALTH_INTERVAL`, дедлайн пробы — `GRPC_HEALTH_PROBE_TIMEOUT`) + опц. reflection, сервисы через fx-группу `grpckit.services`, расширение через `WithServerOptions`/`WithUnaryInterceptors`; `testkit/grpctest` |
| [`natskit`](natskit/) | Pub/sub поверх NATS/JetStream: `Bus`/`StreamBus` (`Publish` байтами + `PublishJSON[T]`), `SubscribeJSON[T]` → `Subscription`, подписка на жизненном цикле fx (`SubscribeOnStart`), graceful drain, ретраи JetStream с растущей задержкой (бюджет по умолчанию — 15 минут), DLQ с заголовками причины, `ErrTerminal`, deadline обработчика, backpressure, аутентификация (`NATS_CREDS_FILE`/`NATS_NKEY_SEED_FILE`/`NATS_TLS_CA_FILE`), несколько шин через `Module(name)`; фейк `testkit/natstest` |
| [`httpclient`](httpclient/) | HTTP-клиент: retry только идемпотентных запросов (`GetBody`, `Retry-After`), circuit breaker на хост, политика редиректов (`MaxRedirects`, запрет кросс-хостовых), OTel-спан на вызов, `X-Request-ID` из ctx, SSE-стримы с лимитом события, `Close` на `OnStop`; `NewConfigPrefixed` для нескольких клиентов, общий разбор заголовка — `ParseRetryAfter`; `http_client_*` |
| [`amocrmkit`](amocrmkit/) | Транспорт к amoCRM: REST v4 (Bearer) + amoJo Chat (HMAC-SHA1) + Drive (multipart-сессии) + OAuth2 (`OAuthConfig`, `TokenPair`, `RefreshCoordinator`); геттеры сущностей, в т.ч. `GetTask` (`endpoint="tasks.get"`); валидация subdomain/TLD, per-account rate-limit с паузой по 429, мутации не ретраятся, таксономия ошибок; `amocrm_api_calls_total{endpoint,status}`, `amocrm_api_duration_seconds`, `amocrm_rate_limited_total{tld,result}`, `amocrm_oauth_refresh_total{grant,status}` |
| [`anthropickit`](anthropickit/) | Клиент Anthropic (`anthropic-sdk-go`): `API` (`Complete`/`Stream`/`CountTokens`/`DefaultModel`), нормализованный `Usage` + `Cost` (`USDMicros`, флаг известной цены), спан `anthropic.messages` с `gen_ai.*`-атрибутами, метрики вызовов, opt-in `Probe`; `testkit/anthropictest` |
| [`grpcclient`](grpcclient/) | gRPC-клиент: `*grpc.ClientConn` + otel (StatsHandler), access-log (в т.ч. стримы), дедлайн по умолчанию (`GRPC_CLIENT_TIMEOUT`), сквозной request-id в `x-request-id`, keepalive, TLS (`GRPC_CLIENT_TLS_CA_FILE`/`GRPC_CLIENT_TLS_SERVER_NAME`), `PerRPCCredentials` (bearer-токен из env, поверх plaintext — только с `GRPC_CLIENT_ALLOW_INSECURE_TOKEN`) |
| [`cachekit`](cachekit/) | Redis ≥ 7.0 (major сверяется на старте — `ErrServerVersion`, снимается `REDIS_SKIP_VERSION_CHECK`; cluster-режим не поддерживается): `Cache` (`Get`/`Set`/`SetNX`/`GetDel`/`CompareAndDelete`/теги), `GetJSON[T]`/`SetJSON[T]`, `IdempotencyStore` для `httpkit`, `MemoryCache` для тестов и single-replica, префикс ключей (`REDIS_KEY_PREFIX`); `cache_commands_*` и спаны `redis <cmd>` |
| [`healthkit`](healthkit/) | Именованная проба `Probe{Name, Check}` (`New`, `WithTimeout`, `Run`; panic проверки — `*PanicError`) и `healthkit.Provide` в fx-группу `healthkit.probes`; `/ready` в `httpkit` и health-loop `grpckit` собирают пробы китов |
| [`flagkit`](flagkit/) | Feature flags из ENV (`FEATURE_FLAGS=foo=true,bar=baz`), мусор в переменной валит старт; `Store.Bool/String/Names` |
| [`timerkit`](timerkit/) | Periodic jobs через fx-группу `timerkit.Group` (регистрация — `timerkit.Provide`, требует `timerkit.Module` в графе): валидация на старте, ожидание джобов на стопе, `Timeout`/`Jitter`/`RunOnStart`, часы из `clockkit`; спан `timerkit.job <name>`, `timer_job_*` |
| [`telemetry`](telemetry/) | Opt-in bootstrap OTel SDK: tracer/meter/logger providers + OTLP HTTP-экспортёры (`OTEL_EXPORTER_OTLP_ENDPOINT` — URL по спецификации, `OTEL_SDK_DISABLED` — общий выключатель); `log.LoggerProvider` для `logkit`, ошибки экспорта — в `logkit`. Без него kit-инструментация no-op |
| [`logkit`](logkit/) | `Logger` поверх slog: JSON или text (`LOG_FORMAT`) в stdout (`LOG_SILENT`) + OTel log-bridge, redact PII на уровне атрибутов (включён всегда, выключатель `LOG_REDACT_DISABLED`; дефолты знают Bearer/Basic/JWT/API-ключи и hex-токены, значение под ключом-секретом — целиком), `trace_id`/`request_id` из ctx, `RegisterAttr(key, fn)` (значение любого типа, без slog у потребителя), позиция вызывающего (`LOG_SOURCE`); события fx в тот же лог — `FxLogger`; `log_messages_total`; capture-логгер `testkit/logtest` |
| [`authnkit`](authnkit/) | `Hasher` (Argon2id с пределом одновременных вычислений, строгий PHC, `NeedsRehash`, `VerifyDummy`); HS256 JWT на go-jose с типом токена (`SignAccess`/`ParseAccess`, `JWT_LEEWAY`), `iss`/`aud` из конфига (`JWT_ISSUER`, `JWT_AUDIENCE`) и ротацией секрета (`JWT_PREVIOUS_SECRET`); `TOTP` с защитой от replay (`VerifyOnce`, `TOTP_SKEW`); одноразовые токены (`NewToken` — 256 бит hex, `HashToken` — SHA-256 для хранения); сессии refresh `authnkit/session` (ротация, отзыв сессии при повторе токена, абсолютный срок, `RevokeAll` в транзакции из ctx; `session/pgstore` — Postgres, миграции и проверка схемы на старте, `SESSION_*`); RBAC `Registry`; `authn_*` |
| [`oidckit`](oidckit/) | OIDC BFF (Authorization Code + PKCE S256): `Verifier` (discovery, JWKS-кэш, iss/aud/azp/exp/nonce, `OIDC_LEEWAY`), роуты login/callback/me/logout, cookie-сессия HS256 (`__Host-`-имена, `Secure` по умолчанию), `Extractor` → `httpkit.AuthExtractor`, `StateStore` (Redis, атомарный `GetDel`), health-проба; требует `*httpclient.Client` в графе; `oidc_*`; stub IdP — `testkit/oidctest` |
| [`hubkit`](hubkit/) | SDK хаба stit: `sso` (verify hub-session двух контуров, обязательный `aud`, JWKS-кэш с grace, `Identity` в ctx, гейты), `issuer` (ES256, три слота ключей и JWKS-handler, rotating-refresh с reuse-detection поверх `cachekit`), `client` (типизированный hub API поверх `httpclient`), `sync` (pull-синк проекций в job'ы `timerkit`), `projection` (готовый `pgkit.Source` — `projection.Migrations`), `hubclaims` (имена claim'ов и контуры hub-session JWT — общий контракт `sso` и `issuer`, без зависимостей); `testkit/hubtest` |
| [`bizmetric`](bizmetric/) | Helper для бизнес-метрик: `Counter/Histogram/Gauge/UpDownCounter`, форсит конвенцию `biz_<domain>_<event>_*` (panic при нарушении), общие бакеты у гистограмм |
| [`outboxkit`](outboxkit/) | Transactional outbox: тик воркера в одной транзакции (`batch` — SKIP LOCKED между репликами, `ordered` — один активный воркер), публикация в JetStream с дедупом, экспоненциальный backoff и poison→dead с `ReviveDead` (конечный бюджет попыток, сбой брокера их не тратит), JetStream и стримы — проверка старта, предел payload, партиции на старте, retention через `DETACH … CONCURRENTLY` (Postgres ≥ 14); `BacklogReporter`; `outbox_*{instance}`; несколько инстансов через `outboxkit.Instance(name, table)` (схема таблицы — в `pgkit.migrations`) + `pgstore.FactoryModule`; recorder (только в транзакции) — `testkit/outboxtest` |
| [`auditkit`](auditkit/) | Audit-log: `Entry`/`Logger` с обязательными `Action`/`Resource` и лимитами полей, core+pgstore split, запись участвует в транзакции из ctx, пустые IP и request-id — из ctx запроса (`Entry.Enrich`), время — часы БД; `audit_log_writes_total`, `audit_log_write_duration_seconds`; `testkit/audittest` |
| [`bruteforcekit`](bruteforcekit/) | Brute-force guard: атомарный учёт попытки до проверки секрета (`Guard.Attempt` + `RecordSuccess`) по паре (subject в нижнем регистре, канонический IP, IPv6 по `/64`), `Cleaner`, `LockState{Until, FailedCount}`, новое окно после истечения lock'а, cleanup-job в группе `timerkit.Group`; `bruteforce_checks_total`, `bruteforce_locks_total`; `testkit/bruteforcetest` |
| [`clockkit`](clockkit/) | `Clock` (`Now`/`Since`/`After`/`NewTimer`) + test-fakes (`Fixed`, `Tickable` с пробуждением таймеров) для детерминированных временных тестов |
| [`tzkit`](tzkit/) | Канон времени: `Zone` (`APP_TZ`, fail-fast, без дефолта), `Day` и `WallTime` с huma-схемой и pgtype-интеропом, границы суток в зоне приложения (корректные в дни перевода стрелок) |
| [`ptrkit`](ptrkit/) | Generic-хелперы указателей: `Of` (адрес значения), `OmitZero` (nil-on-zero), `Deref` (nil-safe) |
| [`mapkit`](mapkit/) | Декодеры `map[string]any`: `String/Int/Int64/Float/Strings/Map/IntPtr/BoolPtr`; nil-safe, знают `json.Number`, значение вне диапазона типа — несовпадение |
| [`idkit`](idkit/) | Внутренние id: `NewV7()` — UUID v7 строкой (time-sortable), `Validate`/`IsV7` — проверка канонической формы |
| [`config`](config/) | Композиция ENV-источников (`Source`, `Load`, `Env`/`EnvPrefixed`) для App-конфига приложения; ошибки всех источников объединяются |
| [`testkit`](testkit/) | testcontainers: `StartPostgres(t, WithMigrations(...))`, `StartNATS(t, WithJetStream())`, `StartRedis`, `StartMinIO`, `RequireDocker`, лесенка миграций `MigrationLadder`; сабпакеты `testkit/<name>test` — `anthropictest`, `audittest`, `authntest`, `bruteforcetest`, `grpctest`, `handlertest`, `hubtest`, `logtest`, `metrictest`, `natstest`, `oidctest`, `outboxtest`, `pgtest`, `sessiontest`, `storagetest` |

Служебные пакеты `internal/otelkit` (scope, версия инструментации, `Must` и две
общие шкалы bucket-границ для гистограмм длительности — `DurationBuckets` и
`LongDurationBuckets`), `internal/requestid`
(ctx-ключ request-id), `internal/clientip` (ctx-ключ IP клиента и свёртка IPv6 в ключ —
общая у лимитера `httpkit` и `bruteforcekit`), `internal/jwks` (JWKS-кэш с троттлингом и grace-периодом
ключа), `internal/httpdoer` (общий интерфейс `Doer`), `internal/grpcstatus`
(классификация gRPC-кодов) и `internal/grpclogkit` (мост grpclog → logkit)
наружу не экспортируются.

## Per-concern endpoints (multi-host)

Каждый кит читает свой эндпоинт из ENV — **разное значение = разный хост**. Это
«шов» для split-топологии: storage / Postgres / NATS / Redis можно
развести по отдельным серверам, **не трогая код**. Репликация / HA / failover —
дело потребителя: runtime проекта ведёт `<org>/infra` (форк
`project-infra-template-compose`).

Таблица полная: `make envdoc-check` требует, чтобы каждая endpoint-переменная
китов (`*_URL`, `*_DSN`, `*_ENDPOINT`, `*_ADDR`, `*_TARGET`) была здесь, а каждая
переменная отсюда существовала в коде с тем же дефолтом. Остальные настройки
кита — в его README § Конфигурация.

### Инфраструктура

| Env | Кит | Дефолт | Назначение |
|---|---|---|---|
| `PG_DSN` | `pgkit` | — (обязателен) | Postgres (primary); `PG_CONNECT_TIMEOUT` (`10s`) — таймаут подключения на старте |
| `PG_REPLICA_DSN` | `pgkit` | — (опц.) | Postgres read-replica (`ReadWriteDB`); пусто → реплики нет |
| `NATS_URL` | `natskit` | `nats://localhost:4222` | NATS / JetStream; userinfo в логах редактируется |
| `REDIS_URL` | `cachekit` | `redis://localhost:6379/0` | Redis ≥ 7.0 (cache + idempotency store + refresh/state store); major сверяется на старте — `REDIS_SKIP_VERSION_CHECK=true` снимает проверку |
| `STORAGE_S3_ENDPOINT` | `storagekit` | — (обязателен) | S3 / MinIO: `host[:port]` или `scheme://host[:port]` |
| `STORAGE_PUBLIC_BASE_URL` | `storagekit` | — (опц.) | база публичных ссылок на объекты (CDN или публичный хост бакета); пусто → `URLBuilder` отдаёт относительный `/<bucket>/<key>` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `telemetry` | `http://localhost:4318` | OTLP HTTP-коллектор, полный URL по спецификации; токен — `OTEL_TOKEN` |
| `OTEL_SDK_DISABLED` | `telemetry` | `false` | общий выключатель SDK: `true` → провайдеры no-op, экспортёры не поднимаются |

### Адреса самого сервиса

| Env | Кит | Дефолт | Назначение |
|---|---|---|---|
| `HTTP_ADDR` | `httpkit` | `:8080` | bind HTTP-сервера; `:0` → порт выбирает ОС (`Server.Addr()`) |
| `GRPC_ADDR` | `grpckit` | `:9090` | bind gRPC-сервера (HTTP/2) |
| `GRPC_CLIENT_TARGET` | `grpcclient` | — (обязателен) | адрес downstream gRPC-сервиса; дедлайн unary-вызова без своего — `GRPC_CLIENT_TIMEOUT` (`10s`); токен — `GRPC_CLIENT_TOKEN`, поверх plaintext требует `GRPC_CLIENT_ALLOW_INSECURE_TOKEN=true`, иначе старт падает |

### Внешние API и SSO

| Env | Кит | Дефолт | Назначение |
|---|---|---|---|
| `ANTHROPIC_BASE_URL` | `anthropickit` | `https://api.anthropic.com` | Anthropic API host; ключ — `ANTHROPIC_API_KEY` |
| `HUB_BASE_URL` | `hubkit/client` | — (обязателен) | hub API company-backend; m2m-токен — `HUB_SERVICE_TOKEN` |
| `HUB_EMPLOYEE_BASE_URL` | `hubkit/client` | — (опц.) | employee-backend, если он на отдельном хосте |
| `HUB_TAG_BASE_URL` | `hubkit/client` | — (опц.) | tag-backend, если он на отдельном хосте |
| `HUB_SSO_JWKS_URL` | `hubkit/sso` | — (обязателен) | JWKS employee-backend для verify сессии сотрудника; `HUB_SSO_AUDIENCE` обязателен |
| `HUB_SSO_LOGIN_URL` | `hubkit/sso` | `https://id.stit.tech/auth/login` | куда редиректить неаутентифицированного сотрудника |
| `HUB_SSO_CUSTOMER_JWKS_URL` | `hubkit/sso` | — (опц.) | JWKS company-backend для сессии заказчика; пусто → контур выключен; при включённом обязателен `HUB_SSO_CUSTOMER_AUDIENCE` |
| `HUB_SSO_CUSTOMER_LOGIN_URL` | `hubkit/sso` | `https://hub.stit.tech/customer/login` | login-редирект контура заказчика |
| `OIDC_ISSUER` | `oidckit` | — (обязателен) | OIDC-провайдер: discovery, token endpoint, JWKS |
| `OIDC_REDIRECT_URL` | `oidckit` | — (обязателен) | абсолютный callback-URL, зарегистрированный у провайдера |
| `OIDC_POST_LOGIN_URL` | `oidckit` | `/` | локальный путь после успешного входа (fallback для `return_to`) |

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

Инструменты кит создаёт в конструкторе (`New*`, сборка `Module`) от текущего
глобального провайдера, а не на уровне пакета. Созданные до первого
`SetMeterProvider` делегат OTel переносит на этот провайдер; второй провайдер
процесса видит только объекты, собранные после его установки. Запись метрик в
тестах проверяет `testkit/metrictest`: метрики китов — провайдер процесса
(глобальный, его ставит первый `Collect`) и дельты серий, бизнес-метрики сервиса
(`Meter` передают) — провайдер теста `NewProvider`; `Collisions` ищет коллизии имён
после трансляции в Prometheus.

Конфиг каждого кита провайдит caller — `Module` его не читает. Пример ниже
собирается целиком (граф проверяется тестом `readme_example_test.go`):

```go
fx.New(
  fx.Provide(
    telemetry.NewConfig, logkit.NewConfig, logkit.NewRedactConfig, httpkit.NewConfig,
    pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig, natskit.NewConfig,
    cachekit.NewConfig, outboxkit.NewConfig, pgstore.NewConfig,
  ),
  telemetry.Module,     // OTLP-экспортёры; без него всё ниже — no-op
  logkit.Module,        // log_messages_total{level}, slog с trace_id в stdout + OTLP, маскирование секретов
  logkit.FxLogger,      // события fx — в тот же лог: ошибки ERROR, рутина DEBUG
  httpkit.Module,       // http_requests_total, http_request_duration_seconds, server-span
  pgkit.Module,         // pg_pool_*{role}, pg traces
  pgkit.MigrateModule,  // миграции группы pgkit.migrations под advisory-lock
  natskit.Module(""),   // nats_messages_*, nats_handle_duration_seconds, consumer-span
  pgstore.Module,       // репозиторий outbox'а, партиции и retention
  outboxkit.Module,     // outbox_*{instance}; без NATS_JETSTREAM_ENABLED=true старт падает
  cachekit.Module,      // cache_commands_total, cache_command_duration_seconds
  timerkit.Module,      // timer_job_runs_total, timer_job_duration_seconds, спан timerkit.job
  healthkit.Provide(pgkit.Probe),
  healthkit.Provide(natskit.Probe),
  healthkit.Provide(cachekit.Probe),
)
```

Имена метрик — Prometheus-стиль `<kit_prefix>_<what>_<unit|total>`; label'ы —
конечные множества (route-pattern, хост, инстанс, результат), не пути и не id.
Гистограммы длительности — в секундах, с общими bucket-границами: по умолчанию
`internal/otelkit.DurationBuckets` (`0.005 … 30`). Для операций, штатно идущих
минутами, есть вторая именованная шкала `otelkit.LongDurationBuckets`
(`0.005 … 600`) — ею пользуется `anthropickit`. Собственных границ киты не заводят.

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

Публичные конфиг-структуры китов (их поля и envconfig-теги), экспортируемые
типы и сигнатуры — **part of public API**. Полный перечень полей — в README
каждого кита § Конфигурация (у сложных китов — `docs/configuration.md`);
`make envdoc-check` (`scripts/envdoc`) требует на каждую переменную кода
(envconfig-тег или `os.Getenv` в любом каталоге, включая `internal/`) строку
таблицы в README или `docs/` её кита с тем же дефолтом, не пускает в доки
переменные, которых нет в коде, сверяет корневую env-таблицу и отвергает поле
конфига без тега, которое envconfig прочитал бы по имени поля (явный отказ —
`ignored:"true"`).

Совместимость **между minor-версиями `v1` не гарантируется**: пока библиотека
идёт вместе со своими потребителями внутри stit, breaking-изменения выпускаются
в minor-релизе. Каждое такое изменение помечается `!` в Conventional Commit и
перечисляется в аннотации тега вместе с шагом миграции. Module path остаётся без
суффикса (`git.stit.tech/stit-core/golibinfra`) — обновление идёт через
`go get @vX.Y.Z`, а не через смену import path.

## Версионирование

Семвер по форме (`vX.Y.Z`), политика:

- **minor** — новая функциональность и breaking-изменения; каждое breaking
  названо в аннотации тега (`Breaking` по китам: что изменилось → как мигрировать);
- **patch** — фиксы без изменения публичных сигнатур, дефолтов, имён метрик и
  envconfig-тегов;
- **major** — смена module path (`/vN`); резервируется на случай, когда
  потребители перестанут обновляться синхронно с библиотекой.

Релиз — skill `/release`: `make gate-clean` → аннотированный тег с release notes
(`Breaking` / `Поведение` / `Изменения` / `Гейт`) → push. `Breaking` — всё, что
требует правки кода или тестов потребителя; `Поведение` — меняется рантайм без
правок (дефолт, исход); `Изменения` — новое, которое можно не трогать. В проектах
после релиза:
```bash
go get git.stit.tech/stit-core/golibinfra@vX.Y.Z && go mod tidy
```

## Гейт

```bash
make tools        # golangci-lint + govulncheck пиненных версий
make hooks        # один раз: core.hooksPath=scripts/git-hooks — pre-push гоняет make check
make check        # hooks-check + fmt-check + vet + lint + test-full + build + tidy-check + govulncheck + envdoc-check + plan-check
make test-full    # unit + integration (testcontainers: Postgres/NATS/Redis/MinIO) -race; без Docker — FAIL
make test         # быстрый цикл: unit -short -race, без контейнеров
make gate-clean   # make check в чистом worktree от HEAD — перед тегом
```

`make check` требует Docker (integration на testcontainers: без Docker тест
падает, а не пропускается) и сети (`govulncheck` тянет базу уязвимостей). Он
всегда проверяет всё дерево и заканчивается строкой `✓ check clean (./...)`:
`PKGS`, `GO` и `A` до шагов не доходят, а с `-i`/`-n`/`-q`/`-t`, `--eval`, вторым
makefile (`-f`, в том числе `Makefile` другого каталога), `GOFLAGS` (env или `go env -w`) и
подменой `MAKE`/`SHELL`/`.SHELLFLAGS`/`MAKEFILES` гейт не запускается; makefile, который
переопределяет рецепты самого гейта, — уже его правка. Кеш golangci-lint —
свой у каждого checkout'а (`.cache/`), у `gate-clean` — холодный.

Push проходит тот же гейт: pre-push (`scripts/git-hooks/pre-push`) гоняет весь
`make check` на каждом пушимом коммите в свежем worktree, как `make gate-clean`, — без
make-окружения вызывающего, незакоммиченных правок и gitignored-файлов; без активного
pre-push `make check` красный (`hooks-check`, подсказка — `make hooks`). Обход — только
руками человека (`git push --no-verify`). Из сессии Claude Code удобные обходы блокируют
deny `.claude/settings.json` (`--no-veri…` где угодно в команде, в том числе в кавычках и
алиасе) и `.claude/hooks/guard-bash.sh` (`git commit -n`, смена `core.hooksPath`, по
`scripts/git-hooks` — всё, кроме чтения) — страховка от удобного обхода, а не от обфускации.

Дополнительно: `make coverage` (покрытие `-short` + сводка), `make bench`
(бенчмарки), `make fuzz` (короткий прогон каждого Fuzz-таргета, `FUZZTIME=10s`),
`make test-timing` (timing-тест logkit: под нагрузкой флакает, поэтому вне гейта).

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
