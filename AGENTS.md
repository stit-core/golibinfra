# AGENTS.md

Инфраструктурные Go-библиотеки для stit-сервисов: observability/auth/db/messaging-кит'ы. Импортируется как Go-модуль из бизнес-репозиториев (`go get git.sftbmb.ru/sftbmb-core/golibinfra@v0.x.y`).

Этот файл — постоянный гайд для агента. Полная human-доку — `README.md` (включая Minimum viable observability, invariants, API stability, версионирование). `AGENTS.md` фокусируется на том, что меняется при работе с кодом lib'ы.

## Где это в экосистеме

**Роль:** общая Go-библиотека. Не сервис, не stack — модули, которые консумят stit-сервисы.
**Зависит от:** — (только upstream Go-deps: pgx/v5, otel-go, huma/v2, sony/gobreaker, nats.go, …).
**Используется:** все stit Go-сервисы (`sftbmb-core/go-backend-template` и все его форки `<client-org>/backend`, плюс будущие worker/billing/…). Кит'ы коммитят на корп-инфра-конвенции (ADR-0004 invariants по `tenant` / `service.namespace`), но **runtime их не выставляет** — это работа корп `otel-collector`'а в `sftbmb-infra/infra`.

Карта 4 репо и канонические контракты — `sftbmb-infra/infra/docs/ecosystem.md`. Observability-invariants и кто что эмитит — `README.md §Minimum viable observability`.

## Пакеты (короткая карта)

Полный список с эмитимыми сигналами — `README.md §Что эмитит каждый пакет`. Здесь — только slug + одна строка ответственности:

| Пакет | Ответственность |
|---|---|
| `telemetry` | OTel SDK bootstrap (resource attrs, exporters, runtime instrumentation, stdout-fallback для local-dev) |
| `logkit` | slog-logger с PII-redact'ом и инжекцией `trace_id`/`span_id` в record |
| `httpkit` | HTTP-server (huma/v2 + chi) + middleware-chain (auth, rate-limit, body-cap, security headers, idempotency, metrics) |
| `httpclient` | HTTP-client с retry + circuit breaker (sony/gobreaker) + otelhttp transport |
| `pgkit` | pgx/v5 pool + read-replica + session timeouts + otelpgx tracer + pool metrics |
| `natskit` | NATS pub/sub + JetStream + backpressure + trace-propagation через message headers |
| `outboxkit` | Outbox-pattern (core + pgstore), lag-reporter |
| `authnkit` | JWT verify/sign + Argon2id hasher + TOTP + RBAC helpers |
| `cachekit` | Redis client + `IdempotencyStore` + tag-based invalidation |
| `bizmetric` | Helper для business-метрик с panic-guard'ом по конвенции `biz_<domain>_<event>_*` |
| `auditkit` | Audit log (core + pgstore) |
| `bruteforcekit` | Brute-force guard (per-subject + per-IP) |
| `flagkit` | Feature flags из ENV |
| `clockkit` | Clock interface + test fakes (Fixed, Tickable) |
| `timerkit` | Periodic jobs через `fx.In` group |
| `config` | Loader-композиция ENV + Vault источников |
| `testkit` | testcontainers helpers (Postgres / NATS / Redis) + sub-package'и под каждый kit |

## Инварианты, на которые завязан корп-стек

Эти три инварианта описаны в `README.md §Обязательные resource attrs` и продублированы здесь как короткий чек-лист:

1. **`service.name`, `service.version`, `deployment.environment.name`** — приложение выставляет через `telemetry.Config`. Без них дашборды Apps/* теряют variable'ы `$service`/`$env`.
2. **`tenant`, `service.namespace`** — приложение **не выставляет**. Их ставит/перезатирает корп `otel-collector` (`sftbmb-infra/infra` ADR-0004 §2, processor `resource/service_namespace` action: upsert). Если выставишь — будет молча перезатёрто.
3. **`biz_<domain>_<event>_total|_seconds|<gauge>`** — единственная конвенция имён для business-метрик. Enforce'ится `bizmetric.Counter/Histogram/Gauge` (panic при нарушении).

Любая правка пакетов `telemetry/`, `logkit/`, `bizmetric/`, или resource-attrs init-флоу должна сверяться с `sftbmb-infra/infra/docs/adrs/0004-observability-tenant-hardening.md` — изменение этих инвариантов = breaking для всех сервисов.

## Стиль

- **gofumpt + golangci-lint** (см. `.golangci.yml`). Линтер гонится локально и в Gitea Actions (`.gitea/workflows/`).
- **fx-first для wiring**. Каждый kit предоставляет `Module`/`<Kit>Module` (`fx.Options`) с конструкторами; consumer (`go-backend-template`) собирает `App` из них. Прямой `New*()` без fx — допустим, но менее идиоматично.
- **envconfig-теги — часть public API.** Структуры конфигов (`pgkit.PoolConfig`, `httpkit.Config`, `telemetry.Config`, и т.д.) перечислены в `README.md §Стабильность API`. Менять имена полей или тег = breaking change → новая мажорная версия.
- **Observability by default.** Любой новый kit должен эмитить метрики/спаны через otel-go SDK с правильными resource attrs (т.е. читать их из `telemetry.Config` через fx, не хардкодить).
- **Comments — только когда WHY неочевидно.** Не пересказывай Go. Описывай нестандартные решения (например, почему пул acquired/idle через ObservableGauge, а не Counter).
- **Минимум зависимостей.** Прежде чем добавить новую сторонюю lib — проверь, нельзя ли решить через stdlib + существующую зависимость (otel-go, pgx, и т.д.). Каждый new import обсуждается с пользователем.

## Релиз

```bash
# подъём минорной версии (pre-v1.0.0 — minor = breaking allowed)
git tag v0.x.y && git push origin v0.x.y
```

В консюмере (`<org>/backend`):

```bash
go get git.sftbmb.ru/sftbmb-core/golibinfra@v0.x.y
go mod tidy
make pre-merge   # обязательный gate перед claim "готово"
```

Локальная разработка с одновременной правкой консюмера — через `replace` директиву в `go.mod` консюмера (см. `README.md §Локальная разработка`).

## Границы

### Делать всегда

- Перед коммитом: `go test ./... -race -short`, `golangci-lint run`, проверить что новые публичные структуры/функции описаны в README или package-комментарии.
- При добавлении нового kit'а — обновить таблицу пакетов в `README.md §Пакеты` и в этом файле (§Пакеты выше).
- При изменении эмитимых метрик / labels / span attrs — обновить `README.md §Что эмитит каждый пакет` **и** `sftbmb-infra/infra/docs/observability-metrics-inventory.md` (актуальный snapshot per-version).

### Сначала спросить

- Менять public конфиг-структуры (имена полей, envconfig-теги, types) — это breaking, нужен явный OK и bump major.
- Менять `telemetry/`, `bizmetric/`, или способ выставления resource attrs — затрагивает все сервисы и инвариант ADR-0004. Сверяй с `sftbmb-infra/infra` доками.
- Добавлять новые зависимости в `go.mod`.

### Не делать никогда

- Выставлять `tenant` или `service.namespace` в `telemetry.Config` дефолтом — они приходят от корп-инфры, не от приложения.
- Включать `:latest` в примеры (как в README, так и в кодовых snippet'ах) — pre-edit hook в потребляющих репо это блокирует, но привычка должна быть.
- Использовать `fmt.Print*` / прямой `slog.Default()` в production-коде — только `logkit.Logger`. Forbidigo в консюмере (`go-backend-template`) ловит это; в lib'е этого нет, но конвенция та же.
- Коммитить product-specific логику. golibinfra — общая lib; всё что специфично для конкретного бизнес-домена (`auth-flow-для-roc-dev`) принадлежит сервис-репо.
