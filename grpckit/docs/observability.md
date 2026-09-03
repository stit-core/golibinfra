# Observability grpckit

Единый источник правды по сигналам кита. `grpckit` не создаёт собственных
otel-инструментов: метрики и спаны эмитит `otelgrpc` StatsHandler
(`grpc.StatsHandler(otelgrpc.NewServerHandler())`), читая meter/tracer из
глобальных OTel-провайдеров (без зарегистрированного провайдера — no-op; экспорт
настраивает потребитель). Access-log пишет `logkit`.

## Метрики (otelgrpc StatsHandler)

StatsHandler эмитит метрику длительности RPC. Имя зависит от семантической
конвенции otelgrpc, выбираемой переменной `OTEL_SEMCONV_STABILITY_OPT_IN`:

| Метрика | Тип | Unit | Когда эмитится |
|---|---|---|---|
| `rpc.server.call.duration` | histogram | `s` | По умолчанию (новая конвенция; `OTEL_SEMCONV_STABILITY_OPT_IN` не задан или без `rpc/*`). |
| `rpc.server.duration` | histogram | `ms` | При `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/old`. |
| обе сразу | histogram | `s` + `ms` | При `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/dup`. |

Гистограмма длительности использует явные бакеты otelgrpc (`0.005 … 10`).

> **Примечание.** StatsHandler этой версии otelgrpc эмитит как метрику **только**
> длительность RPC. Размеры сообщений (request/response size) в метрики не
> попадают — они идут событиями в спан.

### Атрибуты метрик

`rpc.system=grpc`, `rpc.service` (полное имя сервиса), `rpc.method`,
`rpc.response.status_code` (канонический gRPC status). Resource attrs
(`service.name`, `service.version`, `deployment.environment.name`) и экспорт
настраивает потребитель в своей проводке OTel SDK.

## Спаны (otelgrpc StatsHandler)

| Сигнал | Деталь |
|---|---|
| Server span | SpanKind `SERVER`, имя = распарсенный full-method (`package.Service/Method`). |
| Атрибуты | `rpc.system=grpc`, `rpc.service`, `rpc.method`; status выставляется по исходу вызова. |
| Trace-propagation | Входящий трейс-контекст извлекается из metadata; span становится дочерним к вызывающему. |

Корреляция логов с трейсом — через `logkit` (инжектит `trace_id`/`span_id` в
запись), плюс `request_id` из request-id-интерсептора.

## Request-id

Интерсептор берёт `x-request-id` из входящих metadata; невалидный (пустой,
длиннее 128 символов, не печатный ASCII) заменяет на UUID v7. Итоговый id лежит
в `ctx` под ключом `internal/requestid` (тем же, что у `httpkit`) — `logkit`
подхватывает его как `request_id` — и возвращается клиенту в response-header
`x-request-id`, чтобы обе стороны ссылались на один идентификатор.

## Access-log (logkit)

Access-log-интерсептор пишет одну запись на вызов; уровень — по gRPC-коду:

| Исход | Уровень | Сообщение | Поля |
|---|---|---|---|
| `OK` | `Info` | `grpc call` | `method`, `code`, `duration_ms` |
| `Internal`, `Unknown`, `DataLoss`, `Unavailable`, `Unimplemented` | `Error` | `grpc call failed` | `method`, `code`, `duration_ms`, `error` |
| остальные не-OK коды (`InvalidArgument`, `NotFound`, `Unauthenticated`, `PermissionDenied`, `Canceled`, `DeadlineExceeded`, `ResourceExhausted`, …) | `Warn` | `grpc call failed` | `method`, `code`, `duration_ms`, `error` |

`code` — `status.Code(err).String()`; не-status ошибка handler'а — `Unknown`
(`Error`). Паника, пойманная recovery-интерсептором, логируется отдельной записью
`Error` (`grpc panic recovered`: `method`, `panic`, `stack`) и конвертируется в
`codes.Internal`.

Отклонённая аутентификация пишет `Warn` `grpc auth rejected` (`method`, `error`
— текст ошибки `Authenticator`), клиенту уходит только `Unauthenticated:
unauthenticated`.

## Сводка

`grpckit` эмитит `rpc.server.*` (otelgrpc StatsHandler) + server span. Точный
метрик-выход сервера — одна гистограмма длительности (имя по конвенции выше).
