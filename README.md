# golibinfra

Инфраструктурные библиотеки для Go-сервисов компании. Импортируется как Go-модуль из бизнес-репозиториев.

```go
import "git.stit.tech/stit/golibinfra/pgkit"
import "git.stit.tech/stit/golibinfra/httpkit"
import "git.stit.tech/stit/golibinfra/telemetry"
import "git.stit.tech/stit/golibinfra/config"
import "git.stit.tech/stit/golibinfra/testkit"
```

## Где это в экосистеме

`golibinfra` — одна из четырёх компонент stit-экосистемы:

- [`stit/infra`](https://git.stit.tech/stit/infra) — корп-control-plane (Vault, Grafana, OTLP-ingest, Gitea-registry).
- [`stit/project-infra-template-compose`](https://git.stit.tech/stit/project-infra-template-compose) — шаблон проектной VPS (форкается в `<org>/infra`).
- [`stit/go-backend-template`](https://git.stit.tech/stit/go-backend-template) — шаблон Go-бэкенда (форкается в `<org>/backend`, импортирует эту lib'у).
- **`stit/golibinfra`** (вы здесь) — общая lib, observability-by-default, инварианты ADR-0004.

Каноническая карта 4 репо, контракты (tenant = `<p>-<env>`, AppRole `projects-<p>-<env>`, OTLP-flow), happy paths — [`stit/infra/docs/ecosystem.md`](https://git.stit.tech/stit/infra/src/branch/main/docs/ecosystem.md).

## Доступ к репозиторию

Репо приватный, лежит на собственной Gitea (`git.stit.tech`). Чтобы `go get` / `go mod tidy` мог его подтянуть, нужно настроить три вещи: GOPRIVATE, git-аутентификацию и (опционально) SSH-overlay поверх HTTPS.

### Шаг 1. SSH-ключ в Gitea

```bash
# если ключа ещё нет
ssh-keygen -t ed25519 -C "you@stit.tech"

# залить публичный ключ в Gitea: https://git.stit.tech/-/user/settings/keys
cat ~/.ssh/id_ed25519.pub | xclip -selection clipboard

# проверить, что подключение работает
ssh -T git@git.stit.tech
```

### Шаг 2. GOPRIVATE

Сообщаем Go, что `git.stit.tech` — приватный (не идти через `proxy.golang.org`, не проверять `sum.golang.org`):

```bash
go env -w GOPRIVATE='git.stit.tech/*'
```

Альтернативно — переменная окружения в `~/.zshrc` / `~/.bashrc`:
```bash
export GOPRIVATE='git.stit.tech/*'
```

### Шаг 3. SSH вместо HTTPS

Go по умолчанию ходит за модулями через HTTPS. Чтобы использовать SSH-ключ из шага 1 — добавляем git-overlay в `~/.gitconfig`:

```bash
git config --global url."git@git.stit.tech:".insteadOf "https://git.stit.tech/"
```

После этого `go mod tidy` будет ходить за `git.stit.tech/stit/golibinfra` через SSH.

### Альтернатива: HTTPS + личный токен

Если SSH недоступен (CI без ssh-agent и т.п.) — можно через токен:

```bash
# Gitea → Settings → Applications → Generate Token (scope: read:repository)
# Сохранить в ~/.netrc:
cat >> ~/.netrc <<EOF
machine git.stit.tech
login your-username
password ${GITEA_TOKEN}
EOF
chmod 600 ~/.netrc
```

В CI пробрасывай `GITEA_TOKEN` секретом и пиши `.netrc` в job-prepare шаге.

### Проверка

```bash
mkdir /tmp/check && cd /tmp/check && go mod init check
go get git.stit.tech/stit/golibinfra@v0.15.0
# должно скачать без ошибок
```

## Использование в проекте

В `go.mod`:
```
require git.stit.tech/stit/golibinfra v0.15.0
```

`go mod tidy` подтянет.

## Принципы

- **Никакой продуктовой специфики.** Если файл знает про конкретные роли, домены, имена сервисов — он не здесь.
- **Без скрытых зависимостей.** Каждый пакет можно подключать независимо.
- **fx-first.** Каждый пакет экспортирует `Module` для проводки.
- **Observability-by-default.** Все компоненты инструментированы OTel.

## Пакеты

| Пакет | Ответственность |
|---|---|
| [`pgkit`](pgkit/) | `*pgkit.DB` фасад, Transactor, migrate-runner, маппинг pg-ошибок, `DBProbe` health-probe; `ReadWriteDB` для read-replica (`PG_REPLICA_DSN`) |
| [`httpkit`](httpkit/) | Huma bootstrap; middleware: RequestID, Recovery, AccessLog, OTel, CORS, RateLimit, MaxBody, Metrics, HumaAuth, **SecurityHeaders** (HSTS/X-Frame/CSP, auto в Module), **Idempotency** (опц., требует `IdempotencyStore`); `APIError` (`{code,status,title,detail}`), `MapErr`, `MapSentinel`, `ErrorMapping` |
| [`natskit`](natskit/) | Pub/sub поверх NATS: `Bus` интерфейс, `PublishJSON[T]`, `SubscribeJSON[T]`, fx.Module; panic recovery в subscriber'ах + backpressure (semaphore `NATS_MAX_IN_FLIGHT`) |
| [`httpclient`](httpclient/) | HTTP-клиент с retry, circuit breaker (`sony/gobreaker/v2`), OTel-инструментированием и автоматической передачей `X-Request-ID` |
| [`cachekit`](cachekit/) | Redis-обёртка: `Cache` интерфейс, `GetJSON[T]`/`SetJSON[T]`, `InvalidateByTag`; `IdempotencyStore` адаптер для `httpkit` |
| [`flagkit`](flagkit/) | Feature flags из ENV (`FEATURE_FLAGS=foo=true,bar=baz`); `Store.Bool/String` |
| [`timerkit`](timerkit/) | Periodic jobs через fx group: регистрация задач с интервалом, lifecycle-aware |
| [`telemetry`](telemetry/) | OTel init (trace/meter/log), OTLP exporters, `Span(ctx, name)` |
| [`logkit`](logkit/) | `Logger` интерфейс + JSON-реализация поверх slog; trace_id correlation; `ServiceConfig{Service,Env,Version}` — автоматические атрибуты `service/env/version` в каждой записи; `RegisterAttr` для кастомных полей; `NewWithRedact` + `RedactConfig` (PII-фильтры через regex, `LOG_REDACT_ENABLED`) |
| [`authnkit`](authnkit/) | `Hasher` интерфейс + Argon2id-реализация; `JWTConfig` + JWT sign/verify; `TOTPGenerator` (RFC 6238); `Permission`/`RolePermissions`/`Has` для RBAC |
| [`bizmetric`](bizmetric/) | Helper для бизнес-метрик: `Counter/Histogram/Gauge`, форсит конвенцию `biz_<domain>_<event>_*` (panic при нарушении) |
| [`outboxkit`](outboxkit/) | Transactional outbox: Worker, Bus, core+pgstore split, `LagReporter` для `outbox_lag_seconds` |
| [`auditkit`](auditkit/) | Audit-log: `Entry`/`Recorder`, core+pgstore split |
| [`bruteforcekit`](bruteforcekit/) | Brute-force guard: per-subject + per-IP счётчики, `LockState`, core+pgstore split |
| [`clockkit`](clockkit/) | `Clock` интерфейс + test-fakes (`Fixed`, `Tickable`) для детерминированных временных тестов |
| [`config`](config/) | Загрузчик: env + Vault, провайдит `[]Source` для приложения |
| [`testkit`](testkit/) | testcontainers: `StartPostgres`, `StartNATS`, `StartRedis`; httptest helpers; per-kit subpackages |

## Minimum viable observability

Подключение fx-модулей golibinfra даёт **автоматический** RED + DB-pool + bus + logs + traces. Минимальный набор для сервиса, который должен сразу попасть на стандартные дашборды корп-Grafana (папка **Apps**):

```go
fx.New(
  telemetry.Module,   // OTLP exporter + runtime (go_*, process_*)
  logkit.Module,      // log_messages_total{level}, slog с trace_id
  httpkit.Module,     // http_requests_total, http_request_duration_seconds
  pgkit.Module,       // db_pool_* (acquired/idle/max/wait), pg traces
  natskit.Module,     // nats_messages_*, nats_handle_duration_seconds (если NewBusWithObservability)
  outboxkit.Module,   // outbox_events_*, outbox_batch_size, outbox_publish_duration_seconds
  authnkit.Module,    // jwt_verifications_total, password_*_duration_seconds
  ...
)
```

После запуска приложение видно на:
- **Apps · Service Overview / HTTP** — без правок.
- **Apps · Database / Bus / Outbox** — без правок.
- **Apps · Logs** — все `log.Info/Warn/Error/Debug` идут в Loki с trace_id correlation.
- **Apps · Traces** — Tempo получает spans от `otelhttp`, `otelpgx`, `natskit`.

### Обязательные resource attrs

OTel SDK выставляет их через `telemetry.Config`:

| Attr | Значение | Кто ставит | Почему важно |
|---|---|---|---|
| `service.name` | `"backend"` (или другое) | приложение | Variable `$service` в дашбордах |
| `service.version` | semver / git short-sha | приложение | Чтобы дашборды различали deploy'и |
| `deployment.environment.name` | `prod` / `staging` / `smoke` | приложение | Variable `$env` |
| `tenant` | имя tenant'а | **корп otel-collector** (из X-Scope-OrgID) | Multi-tenant изоляция |
| `service.namespace` | == `tenant` (инвариант) | **корп otel-collector** (force-upsert) | Loki tenant-filter (Loki не имеет `tenant` label) |

`tenant` и `service.namespace` приложение не выставляет — их ставит/перезатирает корп otel-collector (`stit/infra` ADR-0004 §2). Если приложение всё же выставит `service.namespace` — оно будет перезатёрто значением `tenant`. Это **инвариант**: ваши логи всегда найдутся через `{service_namespace="<tenant>"}` в Loki.

### Business-метрики (`biz_*`)

Свои метрики бизнес-уровня — через `bizmetric` (форсит конвенцию `biz_<domain>_<event>_total|_seconds|_<gauge>`):

```go
import "git.stit.tech/stit/golibinfra/bizmetric"

logins := bizmetric.Counter(meter, "auth", "login_attempts")
// → имя: biz_auth_login_attempts_total

logins.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "ok")))
```

`Apps · Business` автоматически рендерит repeating-panel'и для всех найденных `biz_*_total`. `result` label почти обязателен — без него не рисуется breakdown ok/error.

Helper упадёт panic'ом если префикс/суффикс не соответствует конвенции — defence-in-depth против "ну я только тут одну метрику".

Полный гайд для проектов: [stit-infra docs/observability-app-guide.md](https://git.stit.tech/stit/infra/src/branch/main/docs/observability-app-guide.md). Инвентарь всех эмитящихся метрик: [stit-infra docs/observability-metrics-inventory.md](https://git.stit.tech/stit/infra/src/branch/main/docs/observability-metrics-inventory.md). Архитектурные решения: [ADR-0002](https://git.stit.tech/stit/infra/src/branch/main/docs/adrs/0002-observability-multitenancy.md), [ADR-0004](https://git.stit.tech/stit/infra/src/branch/main/docs/adrs/0004-observability-tenant-hardening.md).

### Что эмитит каждый пакет (короткий reference)

| Пакет | Метрики | Спаны |
|---|---|---|
| `telemetry` | runtime (`go_*`, `process_*` ≈ 200 метрик) | — |
| `logkit` | `log_messages_total{level}` | — (но slog инжектит `trace_id`/`span_id` в record) |
| `httpkit` | `http_requests_total{method,path,status}`, `http_request_duration_seconds{method,path,status}`, `http_panics_total{path}`, `http_max_body_rejections_total{path}`, `http_idempotency_total{path,result}` | server span от `otelhttp.NewHandler` |
| `httpclient` | `http_client_request_duration_seconds`, `http_client_breaker_transitions_total`, `http_client_breaker_open`, `http_client_retry_attempts_total` | client span от `otelhttp.NewTransport` |
| `pgkit` | `db_pool_{acquired,idle,total,max,constructing}_conns`, `db_pool_{empty_acquire,canceled_acquire,acquire}_total`, `db_pool_acquire_duration_seconds_total` (9 метрик) | per-query span через `otelpgx` |
| `natskit` | `nats_messages_{published,consumed}_total{subject,status[,transport]}`, `nats_{publish,handle}_duration_seconds{subject[,transport]}`, `nats_inflight_messages{subject}` (label `transport=jetstream` добавляется на JetStream-пути) | producer + consumer spans с trace-propagation через NATS headers |
| `outboxkit` | `outbox_events_{inserted,processed}_total{subject,status}`, `outbox_batch_{size,duration_seconds}`, `outbox_publish_duration_seconds{subject}`, `outbox_lag_seconds{subject}` (opt-in через `LagReporter`) | worker span |
| `authnkit` | `jwt_verifications_total{result}`, `password_{hash,verify}_duration_seconds`, `totp_verifications_total{result}` | — |
| `bizmetric` | `biz_<domain>_<event>_total|_seconds|<gauge>` | — |

Все labels автоматически дополняются resource attrs (`service_name`, `service_version`, `deployment_environment_name`, `tenant`, `service_namespace`) — это работа otel-collector'а (`resource_to_telemetry_conversion: true`).

### Anti-patterns

- **Не парсить тело Loki через `| json`.** OTel→Loki экспортер уже распарсил resource/log attrs в **stream labels** (`method`, `path`, `status`, `request_id`, `severity_text`, `detected_level`, …). Тело часто — operation name (`"http"`) или span event, не JSON-сообщение. Фильтр по level — через label `detected_level=~"error"` (lowercase, auto-detected Loki 3.x).
- **Не выставлять `service.namespace` руками.** Bizdocoll force-сетит = tenant. Если выставишь — перезатрётся.
- **Не выпускать `:latest` Docker tag для проектного API.** Шаблонная конвенция — `<sha>` или `<semver>` (см. `stit/go-backend-template`).
- **Не плодить biz-метрики обходя `bizmetric.Counter`.** Helper форсит конвенцию префиксом и panic'ит — это безопасный rail.

## Стабильность API

Публичные структуры конфигов (`pgkit.PoolConfig`, `pgkit.ReplicaConfig`,
`httpkit.Config`, `httpkit.SecurityHeadersConfig`, `httpkit.IdempotencyConfig`,
`telemetry.Config`, `logkit.Config`, `logkit.RedactConfig`, `authnkit.Argon2idParams`,
`cachekit.Config`, `flagkit.Config`, `natskit.Config` (incl. `BackpressureConfig`),
`httpclient.BreakerConfig`) — **part of public API**.
Менять имена/типы полей или их envconfig-теги = breaking change, идёт мажорным
апгрейдом. Продукты собирают свой `App`-конфиг из этих структур и зависят от
их стабильности.

## Версионирование

Семвер. Теги вида `v0.x.y`. До `v1.0.0` — API считается нестабильным, breaking changes допустимы в minor.

```bash
# подъём минорной версии
git tag v0.15.0
git push origin v0.15.0
```

В шаблонах/проектах после релиза:
```bash
go get git.stit.tech/stit/golibinfra@v0.15.0
go mod tidy
```

## Локальная разработка с правкой golibinfra

Когда правишь `golibinfra` параллельно с продуктом — добавь временный `replace` в `go.mod` продукта:
```
replace git.stit.tech/stit/golibinfra => ../golibinfra
```

**Не коммить эту строку** — это для local-only ветки. Перед мержем убрать и поднять версию через `go get`.

## Лицензия

Internal. Stit Tech.
