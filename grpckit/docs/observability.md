# Observability grpckit

Единый источник правды по сигналам кита. `grpckit` не создаёт собственных
otel-инструментов: метрики и спаны эмитит `otelgrpc` StatsHandler
(`grpc.StatsHandler(otelgrpc.NewServerHandler(...))`), читая meter/tracer из
глобальных OTel-провайдеров (без зарегистрированного провайдера — no-op; экспорт
настраивает потребитель). Access-log и внутренние сообщения grpc-go пишет
`logkit`.

## Метрики (otelgrpc StatsHandler)

StatsHandler эмитит метрику длительности RPC. Имя зависит от семантической
конвенции otelgrpc, выбираемой переменной `OTEL_SEMCONV_STABILITY_OPT_IN`:

| Метрика | Тип | Unit | Когда эмитится |
|---|---|---|---|
| `rpc.server.call.duration` | histogram | `s` | По умолчанию (новая конвенция; `OTEL_SEMCONV_STABILITY_OPT_IN` не задан или без `rpc/*`). |
| `rpc.server.duration` | histogram | `ms` | При `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/old`. |
| обе сразу | histogram | `s` + `ms` | При `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/dup`. |

Явные бакеты (`0.005 … 10`, секунды) otelgrpc задаёт только метрике новой
конвенции — `rpc.server.call.duration` (режимы «по умолчанию» и `rpc/dup`).
`rpc.server.duration` (`rpc/old`, миллисекунды) создаётся без опций и получает
дефолтные бакеты OTel SDK (`0 … 10000`); свои границы задаются `View` в проводке
SDK на стороне потребителя.

> **Примечание.** StatsHandler этой версии otelgrpc эмитит как метрику **только**
> длительность RPC. Размеры сообщений (request/response size) в метрики не
> попадают — они идут событиями в спан, и только при
> `otelgrpc.WithMessageEvents(...)` (см. [usage.md](usage.md)).

## Атрибуты метрик и спанов

Набор атрибутов один для метрики и спана и зависит от того же
`OTEL_SEMCONV_STABILITY_OPT_IN`:

| Режим | Атрибуты |
|---|---|
| по умолчанию (новая конвенция) | `rpc.system.name` = `grpc`, `rpc.method` = полное имя (`package.Service/Method`), `rpc.response.status_code` = канонический код в верхнем регистре (OK, NOT_FOUND, DEADLINE_EXCEEDED, …) |
| `rpc/old` | `rpc.system` = `grpc`, `rpc.service` = `package.Service`, `rpc.method` = короткое имя метода, `rpc.response.status_code` |
| `rpc/dup` | объединение обоих наборов; `rpc.method` — полное имя |

Атрибута `rpc.grpc.status_code` эта версия otelgrpc не эмитит ни в одном режиме.
Дашборды и алерты, разбирающие сервис отдельно от метода, требуют `rpc/old` или
`rpc/dup` — в новой конвенции `rpc.service` нет, сервис читается из префикса
`rpc.method`.

Resource attrs (`service.name`, `service.version`, `deployment.environment.name`)
и экспорт настраивает потребитель в своей проводке OTel SDK.

## Спаны (otelgrpc StatsHandler)

| Сигнал | Деталь |
|---|---|
| Server span | SpanKind `SERVER`, имя = распарсенный full-method (`package.Service/Method`). |
| Атрибуты | Набор из таблицы выше + `server.address`/`server.port` локального сокета; статус спана выставляется по исходу вызова. |
| Trace-propagation | Входящий трейс-контекст извлекается из metadata; span становится дочерним к вызывающему. |

Корреляция логов с трейсом — через `logkit` (инжектит `trace_id`/`span_id` в
запись), плюс `request_id` из request-id-интерсептора.

## Request-id

Интерсептор берёт `x-request-id` из входящих metadata; невалидный (пустой,
длиннее 128 символов, не печатный ASCII) заменяет на UUID v7. Итоговый id лежит
в `ctx` под ключом `internal/requestid` (тем же, что у `httpkit`) и возвращается
клиенту в response-header `x-request-id`, чтобы обе стороны ссылались на один
идентификатор.

`grpckit` при импорте регистрирует в `logkit` ctx-атрибут `request_id`, поэтому
поле есть в каждой записи gRPC-only сервиса — импортировать `httpkit` ради этого
не нужно. Регистрация идемпотентна по ключу: сборка с обоими китами не даёт
двух полей. В коде id читается через `grpckit.RequestIDFrom(ctx)`.

## Access-log (logkit)

Access-log-интерсептор пишет одну запись на вызов; уровень — по gRPC-коду:

| Исход | Уровень | Сообщение | Поля |
|---|---|---|---|
| `OK` | `Info` | `grpc call` | `method`, `code`, `duration_ms` |
| `Internal`, `Unknown`, `DataLoss`, `Unavailable`, `Unimplemented` | `Error` | `grpc call failed` | `method`, `code`, `duration_ms`, `error` |
| остальные не-OK коды (`InvalidArgument`, `NotFound`, `Unauthenticated`, `PermissionDenied`, `Canceled`, `DeadlineExceeded`, `ResourceExhausted`, …) | `Warn` | `grpc call failed` | `method`, `code`, `duration_ms`, `error` |

`code` — `status.Code(err).String()`; не-status ошибка handler'а — `Unknown`
(`Error`). Паника попадает в access-log как `codes.Internal` и дополнительно
логируется записью `Error` `grpc panic recovered` (`method`, `panic`, `stack`).

Отклонённая аутентификация пишет `Warn` `grpc auth rejected` (`method`, `error`
— текст ошибки `Authenticator`), клиенту уходит только `Unauthenticated:
unauthenticated`.

Health-вызовы оркестратора идут в access-log наравне с остальными; чтобы убрать
их шум, отфильтруйте их своим интерсептором или отключите трассировку health
через `otelgrpc.WithFilter` (см. [usage.md](usage.md)).

## Health-loop

При непустой fx-группе `healthkit.probes` и `GRPC_HEALTH_INTERVAL > 0` кит переоценивает
пробы и логирует **смену** overall-статуса (не каждый тик):

| Событие | Уровень | Сообщение | Поля |
|---|---|---|---|
| overall стал `NOT_SERVING` | `Warn` | `grpckit: health not serving` | `probe`, `error` |
| overall вернулся в `SERVING` | `Info` | `grpckit: health serving` | — |

Пробы прогоняются последовательно, каждая — с дедлайном
`GRPC_HEALTH_PROBE_TIMEOUT` (`5s`). Зависшая зависимость видна в поле `error`
записи `grpckit: health not serving` как `probe … timed out after …`.

## Внутренние сообщения grpc-go

Логгер grpc-go подменяется мостом при импорте кита (до первого обращения к grpc —
`grpclog.SetLoggerV2` не потокобезопасен), а `New`/`Dial` привязывают к мосту
`logkit.Logger` из графа. Сообщения транспорта (GOAWAY, keepalive, резолвер)
идут записью `grpc` с полем `msg`, а не plain-text в stderr. Уровни: info и
verbose → `Debug`, warning → `Warn`, error → `Error`.

Пока логгер не привязан, мост пишет дефолтным логгером grpc-go (ERROR в stderr;
`GRPC_GO_LOG_SEVERITY_LEVEL`/`GRPC_GO_LOG_VERBOSITY_LEVEL` работают как обычно) —
импорт кита не превращает ошибки транспорта в тишину. `New(cfg, nil)` и
`Dial(cfg, nil)` мост не занимают: `logkit.Nop()` его не забирает, и настоящий
логгер графа получит записи, даже если клиент без логгера собран первым.
Из настоящих логгеров действует последний привязанный — логгер у процесса один.

## Сводка

`grpckit` эмитит `rpc.server.*` (otelgrpc StatsHandler) + server span. Точный
метрик-выход сервера — одна гистограмма длительности (имя по конвенции выше).
