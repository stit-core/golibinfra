# Observability grpckit

Единый источник правды по сигналам кита. `grpckit` не создаёт собственных
otel-инструментов: метрики и спаны эмитит `otelgrpc` StatsHandler
(`grpc.StatsHandler(otelgrpc.NewServerHandler())`), читая meter/tracer из
глобальных провайдеров, которые поднимает `telemetry`. Access-log пишет `logkit`.

## Метрики (otelgrpc StatsHandler)

StatsHandler эмитит метрику длительности RPC. Имя зависит от семантической
конвенции otelgrpc, выбираемой переменной `OTEL_SEMCONV_STABILITY_OPT_IN`:

| Метрика | Тип | Unit | Когда эмитится |
|---|---|---|---|
| `rpc.server.call.duration` | histogram | `s` | По умолчанию (новая конвенция; `OTEL_SEMCONV_STABILITY_OPT_IN` не задан или без `rpc/*`). |
| `rpc.server.duration` | histogram | `ms` | При `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/old`. |
| обе сразу | histogram | `s` + `ms` | При `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/dup`. |

Гистограмма длительности использует явные бакеты otelgrpc (`0.005 … 10`).

> **Примечание.** Эта версия StatsHandler эмитит как метрику **только**
> длительность RPC. Размеры сообщений (request/response size) в метрики не
> попадают — они идут событиями в спан. Корневой README обобщённо называет это
> «`rpc.server.*` (duration, msg-sizes)» — фактический метрик-выход сервера
> ограничен duration.

### Атрибуты метрик

`rpc.system=grpc`, `rpc.service` (полное имя сервиса), `rpc.method`,
`rpc.response.status_code` (канонический gRPC status). Resource attrs
(`service.name`, `service.version`, `deployment.environment.name`, `tenant`,
`service.namespace`) добавляет otel-collector — см. корневой README.

## Спаны (otelgrpc StatsHandler)

| Сигнал | Деталь |
|---|---|
| Server span | SpanKind `SERVER`, имя = распарсенный full-method (`package.Service/Method`). |
| Атрибуты | `rpc.system=grpc`, `rpc.service`, `rpc.method`; status выставляется по исходу вызова. |
| Trace-propagation | Входящий трейс-контекст извлекается из metadata; span становится дочерним к вызывающему. |

Корреляция логов с трейсом — через `logkit` (инжектит `trace_id`/`span_id` в
запись), плюс `request_id` из request-id-интерсептора.

## Access-log (logkit)

Access-log-интерсептор пишет одну запись на вызов:

| Исход | Уровень | Поля |
|---|---|---|
| Успех | `Debug` | `method`, `code`, `duration_ms` |
| Ошибка | `Error` | `method`, `code`, `duration_ms`, `error` |

`code` — `status.Code(err).String()`. Паника, пойманная recovery-интерсептором,
логируется отдельной записью `Error` с полями `method`, `panic`, `stack` и
конвертируется в `codes.Internal`.

## Сводка для дашбордов

В строке корневого README § Что эмитит каждый пакет `grpckit` представлен как
`rpc.server.*` (otelgrpc StatsHandler) + server span. Точный метрик-выход сервера
— одна гистограмма длительности (имя по конвенции выше).
