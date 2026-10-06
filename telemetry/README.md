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
- **OTLP/HTTP по спецификации** — `OTEL_EXPORTER_OTLP_ENDPOINT` — базовый URL
  коллектора, который выбирает потребитель; схема задаёт TLS, per-signal пути
  `/v1/traces`, `/v1/metrics`, `/v1/logs` дописываются к нему. Аутентификация у
  коллектора — `Authorization: Bearer <OTEL_TOKEN>` и/или заголовки из
  `OTEL_EXPORTER_OTLP_HEADERS`.
- **Метрики и трейсы — только OTLP.** В stdout они не пишутся; без живого
  коллектора экспорт дропается, а ошибка экспорта уходит в лог (см. § Ошибки
  экспорта). stdout-копию логов даёт `logkit`.
- **Resource-атрибуты** — `service.name`, `service.version`,
  `deployment.environment.name` из `Config`; `tenant` и `service.namespace` —
  только при непустом `OTEL_TENANT`.
- **Runtime-инструментация** — метрики `go.*` через
  `contrib/instrumentation/runtime`.
- **TextMap-propagator** — `TraceContext` + `Baggage`, в том числе при
  `OTEL_SDK_DISABLED=true`: сквозной trace-контекст между сервисами не зависит
  от экспорта.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/telemetry"
```

Версионирование и установка модуля — в [корневом README](../README.md)
(§ Версионирование, § Стабильность API).

## Быстрый старт

`telemetry.Module` подключается первым — остальные киты пишут в поднятый им
pipeline. Конфиг приложение собирает само или через `telemetry.NewConfig`
(ENV + `Validate`). `logkit.Config` модулю нужен, чтобы построить
stdout-логгер для ошибок экспорта.

```go
fx.New(
	fx.Provide(telemetry.NewConfig), // OTEL_SERVICE_NAME обязателен при включённой телеметрии
	fx.Provide(logkit.NewConfig),
	telemetry.Module,                // Init SDK + OTLP exporters + runtime-метрики; Shutdown на OnStop
	logkit.Module,                   // берёт log.LoggerProvider из графа → логи уходят и в OTLP
).Run()
```

Инструменты киты и приложение берут у глобальных провайдеров
(`otel.Tracer(...)` / `otel.Meter(...)`), а бизнес-метрики — через
[`bizmetric`](../bizmetric/README.md). `Module` эти интерфейсы в граф не кладёт:
проводка потребителя остаётся за потребителем.

```go
func (s *Service) Do(ctx context.Context) (err error) {
	ctx, span := otel.Tracer("myservice").Start(ctx, "Service.Do")
	defer span.End()
	// ...
	return err
}
```

## Конфигурация

`telemetry.Config` — **part of public API**; `NewConfig` и `Init` валидируют
её (`ErrConfig`).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `OTEL_SDK_DISABLED` | `Disabled` | `bool` | `false` | `true` → провайдеры без экспортёров: сигналы дропаются, сети нет; propagator и глобальные провайдеры выставляются |
| `OTEL_SERVICE_NAME` | `ServiceName` | `string` | — (**required**, если не `OTEL_SDK_DISABLED`) | `service.name` |
| `OTEL_SERVICE_VERSION` | `ServiceVersion` | `string` | `dev` | `service.version` — semver / git short-sha |
| `APP_ENV` | `Environment` | `string` | `dev` | `deployment.environment.name` — `prod` / `staging` / `smoke` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `OTLPEndpoint` | `string` | `http://localhost:4318` | базовый URL коллектора: `http://` — без TLS, `https://` — TLS; путь сохраняется, per-signal `/v1/<signal>` дописывается. `host:port` без схемы — ошибка конфигурации; в её тексте userinfo — `xxxxx` |
| `OTEL_TRACES_SAMPLER_ARG` | `SampleRatio` | `float64` | `1.0` | доля сэмплируемых трейсов, `[0, 1]` (`ParentBased(TraceIDRatioBased)`) |
| `OTEL_EXPORT_TIMEOUT` | `ExportTimeout` | `time.Duration` | `10s` | таймаут одного OTLP-запроса у всех трёх экспортёров и их процессоров |
| `OTEL_TRACES_BATCH_DELAY` | `TraceBatchDelay` | `time.Duration` | `5s` | период сброса очереди batch-processor'а трейсов |
| `OTEL_METRICS_INTERVAL` | `MetricInterval` | `time.Duration` | `10s` | период экспорта метрик (он же min-интервал чтения runtime memstats) |
| `OTEL_LOGS_BATCH_DELAY` | `LogBatchDelay` | `time.Duration` | `1s` | период сброса очереди batch-processor'а логов |
| `OTEL_TENANT` | `Tenant` | `string` | `""` | непустой выставляет resource-атрибуты `service.namespace` и `tenant` (одно значение); пусто — их нет |
| `OTEL_EXPORTER_OTLP_HEADERS` | `OTLPHeaders` | `Headers` | — (без заголовков) | заголовки всех трёх экспортёров, формат спецификации `k1=v1,k2=v2` (значения в percent-encoding); ошибка разбора называет ключ, а не значение |
| `OTEL_TOKEN` | `OTLPBearerToken` | `string` | `""` | `Authorization: Bearer <token>` к каждому OTLP-запросу; переопределяет `Authorization` из `OTEL_EXPORTER_OTLP_HEADERS` |

`Validate` отвергает всегда, и при `OTEL_SDK_DISABLED=true`: `SampleRatio` вне `[0, 1]`,
отрицательные таймеры, endpoint без схемы или хоста, невалидное имя заголовка; у
включённой телеметрии — ещё пустой `ServiceName`, нулевые таймеры и пустой endpoint.

### Spec-переменные OTel: что действует

Часть значений кит задаёт явными опциями SDK — они перекрывают
соответствующие переменные спецификации. Остальные доходят до SDK как есть.

| Переменная | Действует | Почему |
|---|---|---|
| `OTEL_RESOURCE_ATTRIBUTES` | да | читает `resource.Default()`; одноимённые атрибуты кита (`service.*`, `deployment.environment.name`) перекрывают её значения |
| `OTEL_EXPORTER_OTLP_COMPRESSION` | да | кит компрессию не задаёт |
| `OTEL_EXPORTER_OTLP_CERTIFICATE` | да | кит TLS-конфиг не задаёт (схема URL задаёт только insecure/secure) |
| `OTEL_BSP_MAX_QUEUE_SIZE`, `OTEL_BSP_MAX_EXPORT_BATCH_SIZE` | да | кит задаёт только задержку и таймаут batch-processor'а трейсов |
| `OTEL_BLRP_MAX_QUEUE_SIZE`, `OTEL_BLRP_MAX_EXPORT_BATCH_SIZE` | да | то же для процессора логов |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` и per-signal аналоги | нет | endpoint собирается китом из `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `OTEL_EXPORTER_OTLP_TIMEOUT` | нет | перекрыт `OTEL_EXPORT_TIMEOUT` |
| `OTEL_BSP_SCHEDULE_DELAY`, `OTEL_BLRP_SCHEDULE_DELAY` | нет | перекрыты `OTEL_TRACES_BATCH_DELAY` / `OTEL_LOGS_BATCH_DELAY` |
| `OTEL_BSP_EXPORT_TIMEOUT`, `OTEL_BLRP_EXPORT_TIMEOUT` | нет | перекрыты `OTEL_EXPORT_TIMEOUT` |
| `OTEL_METRIC_EXPORT_INTERVAL`, `OTEL_METRIC_EXPORT_TIMEOUT` | нет | перекрыты `OTEL_METRICS_INTERVAL` / `OTEL_EXPORT_TIMEOUT` |
| `OTEL_TRACES_SAMPLER` | нет | sampler фиксирован: `ParentBased(TraceIDRatioBased(OTEL_TRACES_SAMPLER_ARG))`. `OTEL_TRACES_SAMPLER_ARG` действует всегда, а не только при `traceidratio` |
| `OTEL_GO_X_DEPRECATED_RUNTIME_METRICS` | да | включает legacy-набор `process.runtime.go.*` в contrib-инструментации |

## Observability

### Что инициализирует

- **TracerProvider** — `ParentBased(TraceIDRatioBased(SampleRatio))`, batcher
  (`TraceBatchDelay`, `ExportTimeout`), экспорт на `<endpoint>/v1/traces`.
- **MeterProvider** — periodic-reader (`MetricInterval`, `ExportTimeout`),
  экспорт на `<endpoint>/v1/metrics`.
- **LoggerProvider** — batch-processor (`LogBatchDelay`, `ExportTimeout`),
  экспорт на `<endpoint>/v1/logs`; выставляется глобальным и отдаётся во fx как
  `log.LoggerProvider`.
- **Composite-propagator** — `TraceContext` + `Baggage` (и при
  `OTEL_SDK_DISABLED`).

### Что эмитит

Своих метрик у кита нет. Единственный источник сигналов — runtime-инструментация
`contrib/instrumentation/runtime`, scope
`go.opentelemetry.io/contrib/instrumentation/runtime`:

| Метрика | Тип | Unit | Атрибуты |
|---|---|---|---|
| `go.memory.used` | UpDownCounter (observable) | `By` | `go.memory.type` = `stack` / `other` |
| `go.memory.limit` | UpDownCounter (observable) | `By` | — (только при заданном `GOMEMLIMIT`) |
| `go.memory.allocated` | Counter (observable) | `By` | — |
| `go.memory.allocations` | Counter (observable) | `{allocation}` | — |
| `go.memory.gc.goal` | UpDownCounter (observable) | `By` | — |
| `go.goroutine.count` | UpDownCounter (observable) | `{goroutine}` | — |
| `go.processor.limit` | UpDownCounter (observable) | `{thread}` | — |
| `go.config.gogc` | UpDownCounter (observable) | `%` | — |

`OTEL_GO_X_DEPRECATED_RUNTIME_METRICS=true` дополнительно включает legacy-набор
`process.runtime.go.*` (`heap_alloc`, `gc.pause_ns`, `goroutines` и т.д.).
Гистограмма `go.schedule.duration` требует отдельной регистрации
`runtime.NewProducer()` на reader'е — кит её не подключает.

### Ошибки экспорта

`Module` заворачивает два канала диагностики SDK в `logkit`-логгер и пишет их
JSON'ом в stdout:

| Канал | Что несёт | Уровень записи |
|---|---|---|
| `otel.SetErrorHandler` | ошибки экспорта (`отказ коллектора`, таймаут) | `error` |
| `otel.SetLogger` | `global.Warn` SDK: «dropped log records», обрезка атрибутов, дубли ключей | `warn` |
| `otel.SetLogger` | `global.Info` / `global.Debug` SDK | `debug` |

Уровни переводятся из шкалы `logr` (`V(1)` — предупреждение, `V(4)`/`V(8)` —
info/debug) в шкалу slog, иначе на дефолтном `LOG_LEVEL=info` второй канал
молчал бы целиком. Повторяющийся текст логируется не чаще раза в минуту, чтобы
недоступный коллектор не заливал лог на каждом батче.

Логгер строится из `logkit.Config` и пишет **только в stdout, без OTel-sink'а**:
иначе ошибка экспорта логов породила бы новую запись, новый экспорт и новую
ошибку. Маскирование при этом работает (`logkit.RedactConfig` из графа —
опционально, без него — дефолтный набор): в текст ошибки экспорта попадают URL
коллектора и атрибуты сообщений SDK. Без
`Module` (прямой `Init` без `WithErrorLogger`) ошибки уходят plain-text'ом в
stderr — так их печатает SDK по умолчанию.

### Resource-атрибуты — кто что ставит

| Attr | Источник | Кто ставит |
|---|---|---|
| `service.name` | `OTEL_SERVICE_NAME` | **приложение** |
| `service.version` | `OTEL_SERVICE_VERSION` | **приложение** |
| `deployment.environment.name` | `APP_ENV` | **приложение** |
| `tenant` | `OTEL_TENANT` | **приложение**, опционально |
| `service.namespace` | `OTEL_TENANT` (то же значение) | **приложение**, опционально |

Пустой `OTEL_TENANT` не выставляет ни одного из двух атрибутов. Если их
проставляет сам коллектор, оставьте переменную пустой — у атрибута должен быть
один источник.

## API

| Символ | Назначение |
|---|---|
| `Config` / `(Config) Validate() error` | конфиг bootstrap'а; `ErrConfig` при невалидных значениях |
| `Headers` / `(*Headers) Decode(string) error` | заголовки OTLP из `OTEL_EXPORTER_OTLP_HEADERS`; `Decode` — разбор значения переменной |
| `NewConfig() (Config, error)` | ENV + `Validate`; ошибка чтения — `ErrConfig` с именем переменной |
| `Module` | fx-модуль: Init providers + `Shutdown` на OnStop, `fx.Invoke` внутри. Принимает `Config`, `logkit.Config` и опционально `logkit.RedactConfig`, provide'ит `*Providers` и `log.LoggerProvider`. **Config не provide'ит** |
| `Init(ctx, cfg, opts...) (*Providers, error)` | bootstrap без fx: providers, глобальные провайдеры, propagator, runtime-инструментация |
| `Option` / `WithErrorLogger(slog.Handler)` | куда уводить ошибки экспорта и внутренние сообщения SDK |
| `Providers` | `TracerProvider` / `MeterProvider` / `LoggerProvider` (SDK-типы) |
| `(*Providers) Shutdown(ctx) error` | гасит tracer → meter → logger, деля дедлайн `ctx` между шагами; ошибки join'ятся, остановка не прерывается |
| `ErrConfig` | sentinel невалидного конфига |

## Тестирование

Метрики и трейсы всегда идут в OTLP — чтобы наблюдать их локально, поднимите
коллектор и нацельте `OTEL_EXPORTER_OTLP_ENDPOINT`. Логи видны в консоли через
`logkit` без коллектора.

Юнит-тесты пакета: сборка per-signal URL, `Validate`/`NewConfig`, парсинг и мерж
заголовков; resource-атрибуты (с `OTEL_TENANT` и без) и терпимость к
`ErrSchemaURLConflict`; `Init` с `OTEL_SDK_DISABLED` (propagator, глобальный
logger provider, спаны без записи); `Init` против `httptest`-коллектора — все три
сигнала приходят на `<base>/v1/{traces,metrics,logs}` с Bearer- и env-заголовками;
`Module` под `fxtest` (Shutdown на OnStop сливает очередь логов, SDK поднимается
и без потребителя `*Providers`, redact доходит до логгера ошибок, граф не
занимает `metric.Meter`/`trace.Tracer`); деление дедлайна `Shutdown` между
сигналами; маршрутизация, уровни и дросселирование диагностики SDK; фактический
список runtime-метрик.

```bash
go test -short -race ./telemetry/
```

## Инварианты и подводные камни

- **`OTEL_SERVICE_NAME` обязателен при включённой телеметрии** — `Init`/`NewConfig`
  возвращают `ErrConfig`. Без корректных `service.*`/`deployment.environment.name`
  сервис не виден на дашбордах.
- **`telemetry.Module` поднимает SDK, даже если графу не нужны `*Providers`.**
  Внутри модуля `fx.Invoke`: глобальные провайдеры, propagator и `Shutdown` на
  `OnStop` не зависят от того, тянет ли кто-то телеметрию по типу. Обратная
  сторона — `telemetry.Config` в графе обязателен, его отсутствие валит сборку.
- **Подключать `telemetry.Module` первым.** Он выставляет глобальные
  провайдеры; инструментация остальных китов без них пишет в no-op.
- **`Module` не отдаёт `metric.Meter`/`trace.Tracer`.** Их провайдит потребитель
  (или берёт `otel.Meter`/`otel.Tracer` напрямую) — библиотечный модуль не
  занимает в графе общие интерфейсы.
- **`OTEL_EXPORTER_OTLP_ENDPOINT` — полный URL.** Схема задаёт TLS, путь
  сохраняется (`https://otlp.example.com/otlp` → `…/otlp/v1/traces`).
- **`fx.StopTimeout` делится на три сигнала.** `Shutdown` даёт каждому провайдеру
  `min(OTEL_EXPORT_TIMEOUT, остаток дедлайна / число оставшихся шагов)` — при
  недоступном коллекторе логи (последние в порядке) всё равно получают живой
  контекст. Держите `fx.StopTimeout` ≥ `OTEL_EXPORT_TIMEOUT`.
- **Расхождение schema URL не валит старт.** При несовпадении semconv кита и
  builtin-детекторов SDK ресурс собирается без schema URL, атрибуты сохраняются.
- **Правка `telemetry` — breaking для всех сервисов, подключивших кит.** `Config`,
  resource-атрибуты и набор сигналов — контракт, на который опираются их env,
  запросы к бэкенду наблюдаемости и дашборды.
