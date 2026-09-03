# telemetry

OTel-bootstrap для сервисов stit: инициализация SDK (tracer / meter / logger
providers), OTLP HTTP-экспорт трёх сигналов в коллектор, resource-атрибуты,
W3C-propagator и runtime-инструментация. Корневой obs-кит — без него
инструментация остальных китов инертна (no-op).

## Назначение

- **Init трёх сигналов** — `TracerProvider`, `MeterProvider`, `LoggerProvider`
  выставляются глобальными (`otel.SetTracerProvider` / `otel.SetMeterProvider`
  / `global.SetLoggerProvider`); `log.LoggerProvider` дополнительно отдаётся во
  fx-граф — `logkit.Module` подхватывает его как второй sink.
- **OTLP/HTTP по спецификации** — `OTEL_EXPORTER_OTLP_ENDPOINT` — базовый URL;
  схема задаёт TLS, per-signal пути `/v1/traces`, `/v1/metrics`, `/v1/logs`
  дописываются к нему. Для корп ingest — `Authorization: Bearer <OTEL_TOKEN>`.
- **Метрики и трейсы — только OTLP.** В stdout они не пишутся; без живого
  коллектора экспорт дропается. stdout-копию логов даёт `logkit`.
- **Resource-атрибуты** — `service.name`, `service.version`,
  `deployment.environment.name` из `Config`; `tenant` / `service.namespace` —
  только при `OTEL_TENANT` (local-dev).
- **Runtime-инструментация** — `runtime.go.*` через
  `contrib/instrumentation/runtime`.
- **TextMap-propagator** — `TraceContext` + `Baggage`, в том числе при
  `OTEL_DISABLED=true`: сквозной trace-контекст между сервисами не зависит от
  экспорта.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/telemetry"
```

## Быстрый старт

`telemetry.Module` подключается первым — остальные киты пишут в поднятый им
pipeline. Конфиг приложение собирает само или через `telemetry.NewConfig`
(envconfig + `Validate`).

```go
fx.New(
	fx.Provide(telemetry.NewConfig), // OTEL_SERVICE_NAME обязателен при включённой телеметрии
	telemetry.Module,                // Init SDK + OTLP exporters + runtime-метрики; Shutdown на OnStop
	fx.Provide(logkit.NewConfig),
	logkit.Module,                   // берёт log.LoggerProvider из графа → логи уходят и в OTLP
).Run()
```

Обёртка операции в спан — `Span(ctx, name)` возвращает дочерний контекст и
finalizer; передайте ему указатель на `err`, чтобы спан получил статус ошибки.
Без записывающего родителя в `ctx` спан стартует от глобального провайдера —
корневые спаны получают и job'ы/воркеры:

```go
func (s *Service) Do(ctx context.Context) (err error) {
	ctx, end := telemetry.Span(ctx, "Service.Do")
	defer end(&err)
	// ...
	return err
}
```

## Конфигурация

`telemetry.Config` — **part of public API**; `NewConfig` и `Init` валидируют
её (`ErrConfig`).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `OTEL_DISABLED` | `Disabled` | `bool` | `false` | `true` → провайдеры без экспортёров: сигналы дропаются, сети нет; propagator и глобальные провайдеры выставляются |
| `OTEL_SERVICE_NAME` | `ServiceName` | `string` | — (**required**, если не `OTEL_DISABLED`) | `service.name` |
| `OTEL_SERVICE_VERSION` | `ServiceVersion` | `string` | `dev` | `service.version` — semver / git short-sha |
| `APP_ENV` | `Environment` | `string` | `dev` | `deployment.environment.name` — `prod` / `staging` / `smoke` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `OTLPEndpoint` | `string` | `http://localhost:4318` | базовый URL коллектора: `http://` — без TLS, `https://` — TLS; путь сохраняется, per-signal `/v1/<signal>` дописывается. `host:port` без схемы — ошибка конфигурации |
| `OTEL_TRACES_SAMPLER_ARG` | `SampleRatio` | `float64` | `1.0` | доля сэмплируемых трейсов, `[0, 1]` (`ParentBased(TraceIDRatioBased)`) |
| `OTEL_EXPORT_TIMEOUT` | `ExportTimeout` | `time.Duration` | `10s` | таймаут одного OTLP-запроса у всех трёх экспортёров и их процессоров |
| `OTEL_TRACES_BATCH_DELAY` | `TraceBatchDelay` | `time.Duration` | `5s` | период сброса очереди batch-processor'а трейсов |
| `OTEL_METRICS_INTERVAL` | `MetricInterval` | `time.Duration` | `10s` | период экспорта метрик (он же min-интервал чтения runtime memstats) |
| `OTEL_LOGS_BATCH_DELAY` | `LogBatchDelay` | `time.Duration` | `1s` | период сброса очереди batch-processor'а логов |
| `OTEL_TENANT` | `Tenant` | `string` | `""` | local-dev: выставляет `service.namespace` + `tenant`; в проде ставит коллектор — не задавать |
| `OTEL_TOKEN` | `OTLPBearerToken` | `string` | `""` | `Authorization: Bearer <token>` к каждому OTLP-запросу |

Таймеры задаются явными опциями SDK, поэтому SDK-стандартные переменные
батчинга/интервалов (`OTEL_BSP_*`, `OTEL_BLRP_*`, `OTEL_METRIC_EXPORT_*`,
`OTEL_EXPORTER_OTLP_TIMEOUT`) не действуют — управляйте через таблицу выше.

## Observability

### Что инициализирует

- **TracerProvider** — `ParentBased(TraceIDRatioBased(SampleRatio))`, batcher
  (`TraceBatchDelay`, `ExportTimeout`), экспорт на `<endpoint>/v1/traces`.
- **MeterProvider** — periodic-reader (`MetricInterval`, `ExportTimeout`),
  экспорт на `<endpoint>/v1/metrics`.
- **LoggerProvider** — batch-processor (`LogBatchDelay`, `ExportTimeout`),
  экспорт на `<endpoint>/v1/logs`; выставляется глобальным и отдаётся во fx как
  `log.LoggerProvider`.
- **Composite-propagator** — `TraceContext` + `Baggage` (и при `OTEL_DISABLED`).

### Что эмитит

Сам `telemetry` эмитит только runtime-метрики (`runtime.go.goroutines`,
`runtime.go.mem.heap_alloc` / `_inuse`, `runtime.go.gc.duration` и т.д.; после
конверсии коллектором — семейство `go_*` / `process_*`). `Span(...)` создаёт
спаны от имени приложения (tracer `"app"`).

### Resource-атрибуты — кто что ставит

| Attr | Источник | Кто ставит |
|---|---|---|
| `service.name` | `OTEL_SERVICE_NAME` | **приложение** |
| `service.version` | `OTEL_SERVICE_VERSION` | **приложение** |
| `deployment.environment.name` | `APP_ENV` | **приложение** |
| `tenant` | — | **корп otel-collector** (из `X-Scope-OrgID`) |
| `service.namespace` | — | **корп otel-collector** (force-upsert, == `tenant`) |

`OTEL_TENANT` выставляет `tenant` / `service.namespace` только для local-dev
или неаутентифицированного коллектора; в проде значение перезатирает коллектор.

## API

| Символ | Назначение |
|---|---|
| `Config` / `(Config) Validate() error` | конфиг bootstrap'а; `ErrConfig` при невалидных значениях |
| `NewConfig() (Config, error)` | envconfig + `Validate` |
| `Module` | fx-модуль: Init providers + `Shutdown` на OnStop; provide'ит `*Providers`, `trace.Tracer`, `metric.Meter` (оба `"app"`), `log.LoggerProvider`. **Config не provide'ит** |
| `Init(ctx, cfg) (*Providers, error)` | bootstrap без fx: providers, глобальные провайдеры, propagator, runtime-инструментация |
| `Providers` | `TracerProvider` / `MeterProvider` / `LoggerProvider` (SDK-типы) |
| `(*Providers) Shutdown(ctx) error` | гасит tracer → meter → logger (логи последними, чтобы ошибки остановки ещё могли быть записаны); ошибки join'ятся, остановка не прерывается |
| `Span(ctx, name, attrs...)` | спан от провайдера записывающего родителя из `ctx`, иначе от глобального; finalizer пишет `RecordError` + `status=Error`, если `*error` не nil |

## Тестирование

Метрики и трейсы всегда идут в OTLP — чтобы наблюдать их локально, поднимите
коллектор и нацельте `OTEL_EXPORTER_OTLP_ENDPOINT`. Логи видны в консоли через
`logkit` без коллектора.

Юнит-тесты пакета: сборка per-signal URL и валидация конфига; `Init` с
`OTEL_DISABLED` (propagator + глобальный logger provider); `Init` против
`httptest`-коллектора — все три сигнала приходят на `<base>/v1/{traces,metrics,logs}`
с Bearer-заголовком; `Span` на `tracetest.SpanRecorder` (корневой спан без
родителя, дочерний с родителем, статус ошибки).

```bash
go test -short -race ./telemetry/
```

## Инварианты и подводные камни

- **Не выставлять `tenant` / `service.namespace` в проде.** Их force-сетит
  корп otel-collector из `X-Scope-OrgID`.
- **`OTEL_SERVICE_NAME` обязателен при включённой телеметрии** — `Init`/`NewConfig`
  возвращают `ErrConfig`. Без корректных `service.*`/`deployment.environment.name`
  сервис не виден на дашбордах.
- **Подключать `telemetry.Module` первым.** Он выставляет глобальные
  провайдеры; инструментация остальных китов без них пишет в no-op.
- **`OTEL_EXPORTER_OTLP_ENDPOINT` — полный URL.** Схема задаёт TLS, путь
  сохраняется (`https://otlp.example.com/otlp` → `…/otlp/v1/traces`).
- **Правка `telemetry` — breaking для всех сервисов.** Это часть корп
  obs-контракта: изменение `Config`, resource-attrs или сигналов затрагивает
  все сервисы и стандартные дашборды.
