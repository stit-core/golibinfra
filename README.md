# golibinfra

Инфраструктурные библиотеки для Go-сервисов компании. Импортируется как Go-модуль из бизнес-репозиториев.

```go
import "git.stit.tech/stit/golibinfra/pgkit"
import "git.stit.tech/stit/golibinfra/httpkit"
import "git.stit.tech/stit/golibinfra/telemetry"
import "git.stit.tech/stit/golibinfra/config"
import "git.stit.tech/stit/golibinfra/testkit"
```

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
| [`config`](config/) | Загрузчик: env + Vault, провайдит `[]Source` для приложения |
| [`testkit`](testkit/) | testcontainers: `StartPostgres`, `StartNATS`, `StartRedis`; httptest helpers |

## Minimum viable observability

Подключение fx-модулей golibinfra даёт **автоматический** RED + DB-pool + bus + logs + traces. Минимальный набор для сервиса, который должен сразу попасть на стандартные дашборды корп-Grafana (папка **Apps**):

```go
fx.New(
  telemetry.Module,   // OTLP exporter + runtime (go_*, process_*)
  logkit.Module,      // log_messages_total{level}, slog с trace_id
  httpkit.Module,     // http_requests_total, http_*_duration_seconds
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

| Attr | Значение | Почему обязательно |
|---|---|---|
| `service.name` | `"backend"` (или другое) | Variable `$service` в дашбордах |
| `service.namespace` | == имя tenant'а | `Apps · Logs` фильтрует по `service_namespace` (Loki не несёт `tenant` как label) |
| `service.version` | semver / git short-sha | Чтобы дашборды могли различить deploy'и |
| `deployment.environment.name` | `prod` / `staging` / `smoke` | Variable `$env` |

`tenant` resource attr ставится корп-otel-collector'ом из `X-Scope-OrgID` header'а — приложение его не выставляет.

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

Полный гайд для проектов: [stit-infra docs/observability-app-guide.md](https://git.stit.tech/stit/infra/src/branch/main/docs/observability-app-guide.md). Инвентарь всех эмитящихся метрик: [stit-infra docs/observability-metrics-inventory.md](https://git.stit.tech/stit/infra/src/branch/main/docs/observability-metrics-inventory.md).

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
