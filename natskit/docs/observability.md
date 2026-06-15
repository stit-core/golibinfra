# Observability natskit

Единый источник правды по телеметрии кита. Имена и labels — точно как в коде
(`instrumentation.go`, `bus.go`, `jetstream.go`).

Meter и Tracer: `git.stit.tech/stit-core/golibinfra/natskit`. Инструменты
создаются в `newNatsInstruments()` (свой набор у `natsBus` и у `jsBus`); ошибка
создания инструмента игнорируется (otel-инструмент-no-op, не panic).

## Метрики

| Метрика | Тип | Labels | Когда / что |
|---|---|---|---|
| `nats_messages_published_total` | counter (`Int64Counter`) | `subject`, `status`, `transport`* | +1 на каждую публикацию. `status` ∈ `ok` / `error`. |
| `nats_publish_duration_seconds` | histogram (`Float64Histogram`, unit `s`) | `subject`, `transport`* | Длительность публикации (Core — вокруг `PublishMsg`; JetStream — до `PubAck`/ошибки). Пишется и при `error`. |
| `nats_messages_consumed_total` | counter (`Int64Counter`) | `subject`, `status`, `transport`* | +1 на каждое обработанное входящее сообщение. `status` ∈ `ok` / `handler_error` / `panic` / `dead`. |
| `nats_handle_duration_seconds` | histogram (`Float64Histogram`, unit `s`) | `subject`, `transport`* | Длительность работы обработчика (включая ack/nak). Пишется при любом исходе. |
| `nats_inflight_messages` | up-down counter (`Int64UpDownCounter`) | `subject`, `transport`* | Сообщения в обработке прямо сейчас (per-subject). +1 на входе в обработчик, −1 на выходе. |

\* Label `transport` присутствует **только у JetStream-пути** (`jsBus`) со
значением `jetstream`. Core-путь (`natsBus`) этот label не ставит — для Core
серии без `transport`.

### Значения label `status`

| `status` | Где | Что значит |
|---|---|---|
| `ok` | publish + consume | Публикация прошла; обработчик вернул `nil`. JetStream при `ok` делает `Ack`. |
| `error` | publish | Сбой публикации (`PublishMsg`/`PubAck`). |
| `handler_error` | consume | Обработчик вернул не-nil ошибку (не `panic`, не исчерпание попыток). В JetStream → `Nak`/`NakWithDelay`. |
| `panic` | consume | Обработчик паникнул — recover поймал, залогировал стек. Core: сообщение теряется. JetStream: NAK (если не исчерпан `MaxDeliver`). |
| `dead` | consume (только JetStream) | Исчерпан `MaxDeliver` → DLQ (если задан) + `Term`. Ределивери остановлен. |

### Семантика label `subject`

`subject` — конкретный NATS-субъект сообщения. Для publish — переданный в
`Publish`/`PublishStream`. Для consume — `msg.Subject()` фактически доставленного
сообщения (важно для wildcard-подписок: серии расщепляются по реальным
субъектам, не по фильтру подписки). Высокая кардинальность субъектов = высокая
кардинальность серий — закладывайте это в схему именования субъектов.

## Спаны

Кит открывает собственные спаны на обоих концах — это включает trace-propagation
через персист JetStream.

| Спан | Kind | Имя | Атрибуты |
|---|---|---|---|
| Producer | `SpanKindProducer` | `natskit.publish <subject>` | `messaging.system=nats`, `messaging.destination.name=<subject>`, `messaging.message.body.size` |
| Consumer | `SpanKindConsumer` | `natskit.handle <subject>` | те же три атрибута |

(Семантические соглашения — `semconv/v1.40.0`.)

- **Producer-спан** открывается в `startPublishSpan`, после чего W3C
  trace-context (`traceparent`/`tracestate`) инжектится в `nats.Header`
  публикуемого сообщения.
- **Consumer-спан** открывается в `startConsumerSpan`: контекст экстрактится из
  header'ов входящего сообщения, спан стартует как child producer-спана. Так
  трасса остаётся связной даже через персист JetStream.
- На обоих концах при ошибке/панике вызывается `span.RecordError` +
  `span.SetStatus(codes.Error, …)`.

## Health-probe

`natskit.Probe(conn)` возвращает `func(context.Context) error` — проверяет
`Conn.Healthy()` (статус `nats.CONNECTED`). `Module` его **не** provide'ит
автоматически (тип коллизит с probe'ами других китов); регистрируйте сами:

```go
fx.Provide(fx.Annotate(natskit.Probe, fx.ResultTags(`group:"health"`)))
```

## Resource attributes

`natskit` **не** выставляет resource attrs (`service.name`, `service.version`,
`deployment.environment.name`, `tenant`, `service.namespace`). Их ставит
приложение через `telemetry.Config` (`service.*`/`environment`) и корп
`otel-collector` (`tenant`/`service.namespace`). Кит читает meter/tracer/
propagator из глобального otel-провайдера (`otel.Meter`/`otel.Tracer`/
`otel.GetTextMapPropagator`).

Все labels автоматически дополняются resource attrs otel-collector'ом
(`resource_to_telemetry_conversion: true`).
