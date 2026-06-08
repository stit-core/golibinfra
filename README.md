<div align="center">

# golibinfra

### Инфраструктурные Go-kit'ы с observability из коробки

Набор переиспользуемых Go-библиотек для сервисов экосистемы stit: телеметрия,
auth, БД, messaging, кэш, аудит, storage. Каждый kit — `fx.Module`,
инструментирован OTel, подключается независимо. Импортируется как Go-модуль из
бизнес-репозиториев.

[![License](https://img.shields.io/badge/license-Apache--2.0-2563eb)](LICENSE)
[![Часть stit](https://img.shields.io/badge/stit-открытые_шаблоны-6366f1)](https://git.stit.tech/stit-core/sftbmb-public)
[![Go](https://img.shields.io/badge/Go-module-00ADD8)](#использование-в-проекте)
[![Observability](https://img.shields.io/badge/observability-OTel_by_default-8b5cf6)](#minimum-viable-observability)
[![Kits](https://img.shields.io/badge/kits-18-0ea5e9)](#пакеты)

</div>

```go
import "git.stit.tech/stit-core/golibinfra/pgkit"
import "git.stit.tech/stit-core/golibinfra/httpkit"
import "git.stit.tech/stit-core/golibinfra/telemetry"
import "git.stit.tech/stit-core/golibinfra/config"
import "git.stit.tech/stit-core/golibinfra/testkit"
```

## Где это в экосистеме

`golibinfra` — общая Go-lib экосистемы stit (5 форкаемых шаблонов + 2 общие
библиотеки). Её импортирует
[`go-backend-template`](https://git.stit.tech/stit-core/go-backend-template)
(форк `<org>/backend`); телеметрия из коробки попадает на стандартные дашборды
корп-Grafana. Карта всех репо — на
[витрине stit](https://git.stit.tech/stit-core/sftbmb-public). Каноническая
карта контрактов (tenant = `<p>-<env>`, AppRole `projects-<p>-<env>`, OTLP-flow)
— [`corp-infra-template/docs/ecosystem.md`](https://git.stit.tech/stit-core/corp-infra-template/src/branch/main/docs/ecosystem.md).

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

Per-kit `README.md` в каталогах китов осознанно **не заводим**: единый источник
правды по каждому киту — эта таблица (ответственность) + § Стабильность API
(public-конфиги) + § Что эмитит каждый пакет (телеметрия). Ссылка
`[<kit>](<kit>/)` ведёт прямо в исходники.

| Пакет | Ответственность |
|---|---|
| [`pgkit`](pgkit/) | `*pgkit.DB` фасад, Transactor, migrate-runner, маппинг pg-ошибок, `DBProbe` health-probe; `ReadWriteDB` для read-replica (`PG_REPLICA_DSN`) |
| [`storagekit`](storagekit/) | S3/object storage: `Storage` (`Put`/`Delete`), `URLBuilder`, in-memory адаптер для тестов, `minio`-подпакет; `Config` + `NewKey` (content-addressed) |
| [`httpkit`](httpkit/) | Huma bootstrap; middleware: RequestID, Recovery, AccessLog, OTel, CORS, RateLimit, MaxBody, Metrics, HumaAuth, **SecurityHeaders**, **Idempotency**; `APIError`, `MapErr`, `MapSentinel` |
| [`natskit`](natskit/) | Pub/sub поверх NATS: `Bus`, `PublishJSON[T]`, `SubscribeJSON[T]`, fx.Module; panic recovery + backpressure (`NATS_MAX_IN_FLIGHT`) |
| [`httpclient`](httpclient/) | HTTP-клиент с retry, circuit breaker (`sony/gobreaker/v2`), OTel и автопередачей `X-Request-ID` |
| [`cachekit`](cachekit/) | Redis-обёртка: `Cache`, `GetJSON[T]`/`SetJSON[T]`, `InvalidateByTag`; `IdempotencyStore` для `httpkit` |
| [`flagkit`](flagkit/) | Feature flags из ENV (`FEATURE_FLAGS=foo=true,bar=baz`); `Store.Bool/String` |
| [`timerkit`](timerkit/) | Periodic jobs через fx group: регистрация задач с интервалом, lifecycle-aware |
| [`telemetry`](telemetry/) | OTel init (trace/meter/log), OTLP exporters, `Span(ctx, name)` |
| [`logkit`](logkit/) | `Logger` поверх slog; trace_id correlation; авто-атрибуты `service/env/version`; `RegisterAttr`; `NewWithRedact` (PII-фильтры) |
| [`authnkit`](authnkit/) | `Hasher` (Argon2id); `JWTConfig` + sign/verify; `TOTPGenerator` (RFC 6238); `Permission`/`RolePermissions`/`Has` для RBAC |
| [`bizmetric`](bizmetric/) | Helper для бизнес-метрик: `Counter/Histogram/Gauge`, форсит конвенцию `biz_<domain>_<event>_*` (panic при нарушении) |
| [`outboxkit`](outboxkit/) | Transactional outbox: Worker, Bus, core+pgstore split, `LagReporter` для `outbox_lag_seconds` |
| [`auditkit`](auditkit/) | Audit-log: `Entry`/`Recorder`, core+pgstore split |
| [`bruteforcekit`](bruteforcekit/) | Brute-force guard: per-subject + per-IP счётчики, `LockState`, core+pgstore split |
| [`clockkit`](clockkit/) | `Clock` + test-fakes (`Fixed`, `Tickable`) для детерминированных временных тестов |
| [`config`](config/) | Загрузчик: env + Vault, провайдит `[]Source` для приложения |
| [`testkit`](testkit/) | testcontainers: `StartPostgres`, `StartNATS`, `StartRedis`; httptest helpers; per-kit subpackages |

## Per-concern endpoints (multi-host)

Каждый кит читает свой эндпоинт из ENV — **разное значение = разный хост**. Это
«шов» для split-топологии: telemetry / storage / Postgres / NATS / Redis можно
развести по отдельным серверам, **не трогая код**. Репликация / HA / failover —
дело потребителя (швы и скиллы — в `corp-infra-template` и
`project-infra-template-compose`).

| Env | Кит | Назначение |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `telemetry` | OTLP-ingest коллектора (пусто / `OTEL_STDOUT_ONLY=true` → stdout) |
| `VAULT_ADDR` | `config` | Vault как secret-source |
| `PG_DSN` | `pgkit` | Postgres (primary) |
| `PG_REPLICA_DSN` | `pgkit` | Postgres read-replica (опц.; `ReadWriteDB`) |
| `STORAGE_S3_ENDPOINT` | `storagekit` | S3 / MinIO object storage |
| `NATS_URL` | `natskit` | NATS / JetStream |
| `REDIS_URL` | `cachekit` | Redis (cache + idempotency store) |

`outboxkit` **не** имеет своего эндпоинта — он co-located с pg-хостом: outbox
пишется в той же транзакции, что и доменные данные (атомарность), поэтому живёт
на том же Postgres, что `PG_DSN`.

## Minimum viable observability

Подключение fx-модулей golibinfra даёт **автоматический** RED + DB-pool + bus +
logs + traces. Минимальный набор для сервиса, который сразу попадает на
стандартные дашборды корп-Grafana (папка **Apps**):

```go
fx.New(
  telemetry.Module,   // OTLP exporter + runtime (go_*, process_*)
  logkit.Module,      // log_messages_total{level}, slog с trace_id
  httpkit.Module,     // http_requests_total, http_request_duration_seconds
  pgkit.Module,       // db_pool_* (acquired/idle/max/wait), pg traces
  natskit.Module,     // nats_messages_*, nats_handle_duration_seconds
  outboxkit.Module,   // outbox_events_*, outbox_batch_size, outbox_publish_duration_seconds
  authnkit.Module,    // jwt_verifications_total, password_*_duration_seconds
  ...
)
```

После запуска приложение видно на дашбордах **Apps · Service Overview / HTTP /
Database / Bus / Outbox / Logs / Traces** — без правок.

### Обязательные resource attrs

| Attr | Значение | Кто ставит |
|---|---|---|
| `service.name` | `"backend"` (или другое) | приложение (`telemetry.Config`) |
| `service.version` | semver / git short-sha | приложение |
| `deployment.environment.name` | `prod` / `staging` / `smoke` | приложение |
| `tenant` | имя tenant'а | **корп otel-collector** (из `X-Scope-OrgID`) |
| `service.namespace` | == `tenant` (инвариант) | **корп otel-collector** (force-upsert) |

`tenant` и `service.namespace` приложение **не** выставляет — их ставит/перезатирает
корп otel-collector (managed на `*.stit.tech` или self-hosted форк
`corp-infra-template`). Инвариант `service_namespace == tenant`: ваши логи всегда
найдутся через `{service_namespace="<tenant>"}` в Loki.

### Business-метрики (`biz_*`)

Свои метрики бизнес-уровня — через `bizmetric` (форсит конвенцию
`biz_<domain>_<event>_total|_seconds|_<gauge>`, panic при нарушении):

```go
logins := bizmetric.Counter(meter, "auth", "login_attempts")  // → biz_auth_login_attempts_total
logins.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "ok")))
```

`Apps · Business` автоматически рендерит панели для всех `biz_*_total`. Полный
гайд — [`corp-infra-template/docs/observability-app-guide.md`](https://git.stit.tech/stit-core/corp-infra-template/src/branch/main/docs/observability-app-guide.md);
инвентарь метрик —
[`observability-metrics-inventory.md`](https://git.stit.tech/stit-core/corp-infra-template/src/branch/main/docs/observability-metrics-inventory.md).

### Что эмитит каждый пакет (короткий reference)

| Пакет | Метрики | Спаны |
|---|---|---|
| `telemetry` | runtime (`go_*`, `process_*` ≈ 200 метрик) | — |
| `logkit` | `log_messages_total{level}` | — (slog инжектит `trace_id`/`span_id`) |
| `httpkit` | `http_requests_total`, `http_request_duration_seconds`, `http_panics_total`, `http_max_body_rejections_total`, `http_idempotency_total` | server span (`otelhttp`) |
| `httpclient` | `http_client_request_duration_seconds`, `http_client_breaker_*`, `http_client_retry_attempts_total` | client span |
| `pgkit` | `db_pool_*` (9 метрик) | per-query span (`otelpgx`) |
| `storagekit` | — | — |
| `natskit` | `nats_messages_*`, `nats_{publish,handle}_duration_seconds`, `nats_inflight_messages` | producer + consumer spans (trace-propagation) |
| `outboxkit` | `outbox_events_*`, `outbox_batch_*`, `outbox_publish_duration_seconds`, `outbox_lag_seconds` (opt-in) | worker span |
| `authnkit` | `jwt_verifications_total`, `password_*_duration_seconds`, `totp_verifications_total` | — |
| `bizmetric` | `biz_<domain>_<event>_total\|_seconds\|<gauge>` | — |

Все labels автоматически дополняются resource attrs otel-collector'ом
(`resource_to_telemetry_conversion: true`).

### Anti-patterns

- **Не парсить тело Loki через `| json`** — OTel→Loki экспортер уже распарсил
  resource/log attrs в stream labels. Фильтр по level — `detected_level=~"error"`.
- **Не выставлять `service.namespace` руками** — otel-collector force-сетит = tenant.
- **Не выпускать `:latest` Docker tag** для проектного API — `<sha>` или `<semver>`.
- **Не плодить biz-метрики обходя `bizmetric.Counter`** — helper форсит конвенцию.

## Стабильность API

Публичные структуры конфигов (`pgkit.PoolConfig`, `pgkit.ReplicaConfig`,
`storagekit.Config`, `httpkit.Config`, `httpkit.SecurityHeadersConfig`,
`httpkit.IdempotencyConfig`, `telemetry.Config`, `logkit.Config`,
`logkit.RedactConfig`, `authnkit.Argon2idParams`, `cachekit.Config`,
`flagkit.Config`, `natskit.Config`, `httpclient.BreakerConfig`,
`config.VaultClientConfig`) — **part of public API**. Менять имена/типы полей или
их envconfig-теги = breaking change, идёт мажорным апгрейдом.

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
входа — **[витрина stit](https://git.stit.tech/stit-core/sftbmb-public)**.
Нужна команда, стандарт или поддержка —
**[наш подход и контакты (vision)](https://git.stit.tech/stit/vision)**.

## Лицензия

**Apache-2.0** — см. [`LICENSE`](LICENSE).
