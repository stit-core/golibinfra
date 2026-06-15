# telemetry

OTel-bootstrap для сервисов stit: инициализация SDK (tracer / meter / logger
providers), OTLP-экспорт всех трёх сигналов в корп-коллектор и выставление
обязательных resource-атрибутов. Это корневой obs-кит — он включает остальные
киты в стандартные дашборды корп-Grafana.

## Назначение

`telemetry` поднимает OpenTelemetry SDK и связывает его с глобальными
OTel-провайдерами процесса, чтобы инструментация остальных китов
(`otelhttp`, `otelpgx`, `otelgrpc`, `logkit`, `bizmetric` и т.д.) писала в один
pipeline:

- **Init трёх сигналов** — `TracerProvider`, `MeterProvider`, `LoggerProvider`
  выставляются как глобальные через `otel.SetTracerProvider` /
  `otel.SetMeterProvider`; logger-provider отдаётся отдельно для `logkit`.
- **OTLP-экспорт по HTTP** — трейсы, метрики и логи уходят на
  `OTEL_EXPORTER_OTLP_ENDPOINT` (per-concern endpoint коллектора). Для корп
  ingest добавляется `Authorization: Bearer <OTEL_TOKEN>`.
- **stdout-fallback** — при `OTEL_STDOUT_ONLY=true` все три сигнала идут в stdout
  (local-dev без коллектора).
- **Resource-атрибуты** — `service.name`, `service.version`,
  `deployment.environment.name` берутся из `Config` (их ставит приложение).
  `tenant` / `service.namespace` приложение **не** выставляет — их ставит корп
  otel-collector (см. § Инварианты).
- **Runtime-инструментация** — стандартные go-runtime метрики (`runtime.go.*`)
  поднимаются автоматически.
- **TextMap-propagator** — composite `TraceContext` + `Baggage` для сквозной
  передачи trace-контекста между сервисами.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/telemetry"
```

Версия модуля и настройка `GOPRIVATE` / доступа к Gitea — в корневом
[`../README.md`](../README.md).

## Быстрый старт

`telemetry.Module` подключается первым — остальные киты пишут в поднятый им
pipeline. Конфиг приложение либо собирает само, либо берёт `telemetry.NewConfig`
(читает ENV через envconfig).

```go
package main

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/telemetry"
	"go.uber.org/fx"
)

func main() {
	fx.New(
		// Config из ENV (OTEL_SERVICE_NAME обязателен).
		fx.Provide(telemetry.NewConfig),

		// Init SDK + OTLP exporters + runtime-метрики; Shutdown на OnStop.
		telemetry.Module,

		// ... остальные киты пишут в поднятый pipeline.
	).Run()
}
```

Конфиг можно собрать и вручную — тогда `telemetry.NewConfig` не нужен:

```go
fx.Provide(func() telemetry.Config {
	return telemetry.Config{
		ServiceName:    "backend",
		ServiceVersion: "v1.4.2",     // semver или git short-sha
		Environment:    "prod",       // prod / staging / smoke
	}
})
```

Обёртка операции в спан — `Span(ctx, name)` возвращает дочерний контекст и
finalizer; передайте finalizer'у указатель на `err`, чтобы спан получил статус
ошибки:

```go
func (s *Service) Do(ctx context.Context) (err error) {
	ctx, end := telemetry.Span(ctx, "Service.Do")
	defer end(&err)

	// ... err, выставленный здесь, попадёт в span.RecordError + status=Error.
	return err
}
```

Сигнатуры:

```go
func NewConfig() (Config, error)
func Init(ctx context.Context, cfg Config) (*Providers, error)
func (p *Providers) Shutdown(ctx context.Context) error
func Span(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(*error))
```

## Конфигурация

`telemetry.Config` — **part of public API**. Имена полей, типы и `envconfig`-теги
менять = breaking change (идёт мажорным апгрейдом, см. § Стабильность API
корневого README).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `OTEL_SERVICE_NAME` | `ServiceName` | `string` | — (**required**) | `service.name` — имя сервиса на дашбордах (`$service`) |
| `OTEL_SERVICE_VERSION` | `ServiceVersion` | `string` | `dev` | `service.version` — semver / git short-sha |
| `APP_ENV` | `Environment` | `string` | `dev` | `deployment.environment.name` — `prod` / `staging` / `smoke` (`$env`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `OTLPEndpoint` | `string` | `localhost:4318` | host:port OTLP-ingest коллектора (per-concern endpoint) |
| `OTEL_EXPORTER_OTLP_INSECURE` | `OTLPInsecure` | `bool` | `true` | `true` → схема `http://`, `false` → `https://` |
| `OTEL_TRACES_SAMPLER_ARG` | `SampleRatio` | `float64` | `1.0` | доля сэмплируемых трейсов (`ParentBased(TraceIDRatioBased)`) |
| `OTEL_EXPORT_TIMEOUT` | `ExportTimeout` | `time.Duration` | `10s` | batch-timeout трейсов, интервал periodic-reader метрик, min-интервал чтения runtime memstats |
| `OTEL_TENANT` | `Tenant` | `string` | `""` | local-dev: выставляет `service.namespace` + `tenant`. В проде ставит коллектор — не задавать (см. § Инварианты) |
| `OTEL_TOKEN` | `OTLPBearerToken` | `string` | `""` | если задан — `Authorization: Bearer <token>` к каждому OTLP-запросу (обязателен для корп ingest) |
| `OTEL_STDOUT_ONLY` | `StdoutOnly` | `bool` | `false` | `true` → все три сигнала в stdout, без OTLP (local-dev / тесты) |

## Observability

### Что инициализирует

- **TracerProvider** — `ParentBased(TraceIDRatioBased(SampleRatio))`, batcher с
  `WithBatchTimeout(ExportTimeout)`, экспорт на `…/v1/traces`.
- **MeterProvider** — periodic-reader с `WithInterval(ExportTimeout)`, экспорт
  на `…/v1/metrics`.
- **LoggerProvider** — batch-processor, экспорт на `…/v1/logs`; отдаётся как
  `*sdklog.LoggerProvider` для `logkit`.
- **Composite-propagator** — `TraceContext` + `Baggage`.

### Что эмитит

Сам `telemetry` эмитит только **runtime-метрики** через
`contrib/instrumentation/runtime` (`runtime.go.goroutines`,
`runtime.go.mem.heap_alloc` / `_inuse`, `runtime.go.gc.duration` и т.д.). На
дашбордах после конверсии otel-collector'ом это семейство `go_*` / `process_*`
(в корневом README — «runtime (`go_*`, `process_*`)»). Своих спанов или
прикладных метрик пакет не эмитит; `Span(...)` создаёт спаны от имени
приложения через провайдер из контекста.

### OTLP flow

OTLP-URL собирается явно: `<scheme>://<OTLPEndpoint>/<signalPath>`, где `scheme`
= `http` при `OTLPInsecure=true`, иначе `https`; `signalPath` = `v1/traces` /
`v1/metrics` / `v1/logs`. При заданном `OTEL_TOKEN` каждый запрос несёт
`Authorization: Bearer <token>`. Дальше сигналы идут на корп otel-collector,
который раскладывает их по per-tenant Loki / Tempo / VictoriaMetrics-потокам.

### Resource-атрибуты — кто что ставит

| Attr | Источник | Кто ставит |
|---|---|---|
| `service.name` | `Config.ServiceName` (`OTEL_SERVICE_NAME`) | **приложение** |
| `service.version` | `Config.ServiceVersion` (`OTEL_SERVICE_VERSION`) | **приложение** |
| `deployment.environment.name` | `Config.Environment` (`APP_ENV`) | **приложение** |
| `tenant` | — | **корп otel-collector** (из `X-Scope-OrgID`) |
| `service.namespace` | — | **корп otel-collector** (force-upsert, == `tenant`) |

`tenant` / `service.namespace` в проде ставит и перезатирает корп
otel-collector; инвариант `service.namespace == tenant` гарантирует, что логи
сервиса находятся через `{service_namespace="<tenant>"}` в Loki. Поле
`Config.Tenant` (`OTEL_TENANT`) выставляет эти атрибуты **только** для local-dev
или неаутентифицированного коллектора — в проде его не задают.

## API

| Символ | Назначение |
|---|---|
| `Config` | конфиг bootstrap'а; envconfig-теги — public API (§ Конфигурация) |
| `NewConfig() (Config, error)` | читает `Config` из ENV через envconfig |
| `Module` | fx-модуль: Init providers + lifecycle Shutdown; provide'ит `*Providers`, `trace.Tracer`, `metric.Meter`, `*sdklog.LoggerProvider`. **Config не provide'ит** — приложение собирает сам или через `fx.Provide(NewConfig)` |
| `Init(ctx, cfg) (*Providers, error)` | прямой bootstrap без fx: поднимает providers, выставляет глобальные OTel-providers + propagator, стартует runtime-инструментацию |
| `Providers` | держит `TracerProvider` / `MeterProvider` / `LoggerProvider` |
| `(*Providers) Shutdown(ctx) error` | gracefully гасит все три провайдера (logger → meter → tracer), join-ит ошибки |
| `Span(ctx, name, attrs...)` | дочерний спан от провайдера в контексте (tracer `"app"`); finalizer пишет `RecordError` + `status=Error`, если переданный `*error` не nil |

`Module` отдаёт в fx-граф `trace.Tracer` и `metric.Meter` (оба `otel.Tracer/Meter("app")`),
зависящие от `*Providers` — это гарантирует порядок инициализации: providers
подняты до того, как кто-то получит tracer/meter.

## Тестирование

`OTEL_STDOUT_ONLY=true` поднимает все три сигнала через stdout-exporters с
simple-процессорами (flush на каждую запись, без батча) — pipeline работает без
живого коллектора. Глобальные providers и propagator выставляются так же, как в
OTLP-ветке, поэтому `otelhttp` / `otelpgx` и прочая инструментация пишут в тот же
stdout, а не в no-op default. Это режим для local-dev и интеграционных тестов:
видно, что сигналы реально формируются и экспортируются.

Юнит-тест пакета (`init_test.go`) покрывает сборку OTLP-заголовков из
`OTLPBearerToken`. Запуск, как в CI:

```bash
go test -short -race ./...
```

## Инварианты и подводные камни

- **Не выставлять `tenant` / `service.namespace` в проде.** Их force-сетит корп
  otel-collector из `X-Scope-OrgID` (`service.namespace` через force-upsert ==
  `tenant`). Выставите сами через `OTEL_TENANT` — значение в проде будет молча
  перезатёрто; `OTEL_TENANT` оставляйте пустым везде, кроме local-dev /
  неаутентифицированного коллектора.
- **`OTEL_SERVICE_NAME` обязателен.** Поле `required:"true"` — без него
  `NewConfig` вернёт ошибку. Без корректных `service.name` / `service.version` /
  `deployment.environment.name` дашборды корп-Grafana (папка **Apps**) теряют
  `$service` / `$env` и сервис на них не виден.
- **Подключать `telemetry.Module` первым.** Он выставляет глобальные
  OTel-providers; инструментация остальных китов без них пишет в no-op default.
- **Правка `telemetry` — breaking для всех сервисов.** Это корневой obs-кит и
  часть корп obs-контракта. Изменение `Config` (имена/типы/теги), resource-attrs
  init-флоу или эмитимых сигналов затрагивает все сервисы и стандартные дашборды
  — сверяйтесь с корп obs-контрактом (`corp-infra-template/docs/ecosystem.md`
  § Tenant identity / Observability resource-attrs) до изменения инвариантов.
- **`OTEL_EXPORTER_OTLP_ENDPOINT` — host:port, без схемы.** Схему определяет
  `OTEL_EXPORTER_OTLP_INSECURE` (`http` / `https`); URL собирается явно, чтобы не
  зависеть от scheme-inference OTLP-экспортёров.
- **`OTEL_EXPORT_TIMEOUT` — один параметр на три таймера.** Он же batch-timeout
  трейсов, интервал periodic-reader метрик и min-интервал чтения runtime
  memstats.
```
