# Observability natskit

Единый источник правды по телеметрии кита. Имена и labels — точно как в коде
(`instrumentation.go`, `bus.go`, `jetstream.go`, `conn.go`).

Meter и Tracer: `git.stit.tech/stit-core/golibinfra/natskit` (через
`internal/otelkit`). Инструменты создаются в `newNatsInstruments(instance)` (свой
набор у `natsBus` и у `jsBus`; SDK склеивает их по имени в один инструмент);
невалидное имя инструмента — баг кита и паника на старте (`otelkit.Must`).
Без зарегистрированного `MeterProvider`/`TracerProvider` сигналы — no-op.

## Метрики

| Метрика | Тип | Labels | Когда / что |
|---|---|---|---|
| `nats_messages_published_total` | counter (`Int64Counter`) | `instance`, `subject`, `status`, `transport`* | +1 на каждую публикацию. `status` ∈ `ok` / `error`. |
| `nats_publish_duration_seconds` | histogram (`Float64Histogram`, unit `s`) | `instance`, `subject`, `transport`* | Длительность публикации (Core — вокруг `PublishMsg`; JetStream — до `PubAck`/ошибки/таймаута). Пишется и при `error`. |
| `nats_messages_consumed_total` | counter (`Int64Counter`) | `instance`, `subject`, `status`, `transport`* | +1 на каждое обработанное входящее сообщение. `status` ∈ `ok` / `handler_error` / `panic` / `dead`. |
| `nats_handle_duration_seconds` | histogram (`Float64Histogram`, unit `s`) | `instance`, `subject`, `transport`* | Длительность работы обработчика (включая ack/nak). Пишется при любом исходе. |
| `nats_inflight_messages` | up-down counter (`Int64UpDownCounter`) | `instance`, `subject`, `transport`* | Сообщения в обработке прямо сейчас. +1 на входе в обработчик, −1 на выходе. |

\* Label `transport` присутствует **только у JetStream-пути** (`jsBus`) со
значением `jetstream`. Core-путь (`natsBus`) этот label не ставит — для Core
серии без `transport`.

### Семантика label `instance`

`instance` — имя инстанса шины (`Module("hub")` → `hub`, дефолтный —
`default`). Приложение с несколькими шинами (своя + внешняя) иначе складывало бы
их сигналы в одну серию: по этому label они расходятся.

### Значения label `status`

| `status` | Где | Что значит |
|---|---|---|
| `ok` | publish + consume | Публикация прошла; обработчик вернул `nil`. JetStream при `ok` делает `Ack`. |
| `error` | publish | Сбой публикации (`PublishMsg`/`PubAck`/`ErrAsyncPublishTimeout`). |
| `handler_error` | consume | Обработчик вернул не-nil ошибку (в т.ч. `context.DeadlineExceeded` по `HandlerTimeout`), не `panic`, не исчерпание попыток. В JetStream → `Nak`/`NakWithDelay`. |
| `panic` | consume | Обработчик паникнул — recover поймал, залогировал стек. Core: сообщение теряется. JetStream: NAK (если не исчерпан `MaxDeliver`). |
| `dead` | consume (только JetStream) | Исчерпан `MaxDeliver` → DLQ (если задан) + `Term`. Ределивери остановлен. |

### Семантика label `subject`

- publish — subject, переданный в `Publish`/`PublishStream`/`StreamMsg.Subject`.
- consume (Core и JetStream) — **subject подписки** (`Subscribe(subject)` /
  `FilterSubject` консьюмера), не реальный subject сообщения. Wildcard-подписка
  `orders.*.created` даёт одну серию, а не по серии на каждый id. Реальный subject
  обработчик читает из `SubjectFromContext(ctx)`; в спанах он есть.

Кардинальность серий = число подписок × инстансов; subjects публикаций
закладывайте с той же оглядкой.

## Спаны

Кит открывает собственные спаны на обоих концах — это включает trace-propagation
через персист JetStream.

| Спан | Kind | Имя | Атрибуты |
|---|---|---|---|
| Producer | `SpanKindProducer` | `natskit.publish <subject>` | `messaging.system=nats`, `messaging.destination.name=<subject>`, `messaging.message.body.size`, `instance` |
| Consumer | `SpanKindConsumer` | `natskit.handle <subject>` (реальный subject сообщения) | те же четыре атрибута |

(Семантические соглашения — `semconv/v1.40.0`.)

- **Producer-спан** открывается в `startPublishSpan`, после чего W3C
  trace-context (`traceparent`/`tracestate`) инжектится в `nats.Header`
  публикуемого сообщения.
- **Consumer-спан** открывается в `startConsumerSpan`: контекст экстрактится из
  header'ов входящего сообщения, спан стартует как child producer-спана. Так
  трасса остаётся связной даже через персист JetStream. `ctx` обработчика —
  child этого спана; в JetStream он несёт deadline `HandlerTimeout`.
- На обоих концах при ошибке/панике вызывается `span.RecordError` +
  `span.SetStatus(codes.Error, …)`.

## Логи

Все записи — через `logkit.Logger` инстанса, с полем `instance`.

| Уровень | Сообщение | Когда |
|---|---|---|
| `error` | `natskit: async error` | Асинхронная ошибка клиента nats.go: slow consumer (поля `subject`, `dropped` — сколько сообщений потеряно), сбой drain, ошибки протокола. |
| `warn` | `natskit: disconnected` | Разрыв соединения (не при `Close`). Поле `err` — причина, если есть. |
| `info` | `natskit: reconnected` | Переподключение; поле `url`. |
| `warn` | `natskit: subscription drain` | Подписка не додренилась в бюджет `Close` / ошибка снятия интереса. |
| `warn` | `natskit: drain timeout, handlers still in flight` | `Close` исчерпал `DrainTimeout`, обработчики ещё работают (поле `inflight`). |
| `warn` | `natskit: drain timeout, closing connection` | Flush публикаций не уложился в остаток бюджета — соединение закрыто принудительно. |
| `warn` | `natskit: jetstream consume error` | Ошибка pull-консьюмера (нет heartbeat, консьюмер удалён, соединение закрыто); поля `stream`, `subject`, `durable`. |
| `warn` | `natskit: jetstream in-progress` | Не удалось отправить `InProgress` — сервер может переотправить сообщение раньше, чем обработчик закончит. |
| `error` | `natskit: handler error` / `natskit: jetstream handler error` | Обработчик вернул ошибку (кроме `context.Canceled`). |
| `error` | `natskit: handler panic` / `natskit: jetstream handler panic` | Паника обработчика, со стеком. |
| `error` | `natskit: jetstream ack error` / `… term error` / `natskit: dlq publish` | Сбой подтверждения или DLQ-публикации. |

## Health-probe

`natskit.Probe(client) healthkit.Probe` проверяет `Client.Healthy()` (статус
`nats.CONNECTED`). `Module` пробу **не** provide'ит автоматически: в readiness
имеет смысл класть не каждый инстанс. Регистрация — `healthkit.Provide(natskit.Probe)`
для дефолтного инстанса или `natskit.HealthProbe(name)` для любого:

```go
natskit.HealthProbe("")      // дефолтный инстанс
natskit.HealthProbe("hub")   // именованный
```

## Resource attributes

`natskit` **не** выставляет resource attrs (`service.name`, `service.version`,
`deployment.environment.name`) — их несёт OTel-resource, настроенный потребителем
в его проводке OTel SDK. Кит читает meter/tracer/propagator из глобального
otel-провайдера (`otel.Meter`/`otel.Tracer`/`otel.GetTextMapPropagator`); без
зарегистрированных провайдеров сигналы — no-op.
