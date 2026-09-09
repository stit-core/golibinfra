# Observability natskit

Единый источник правды по телеметрии кита. Имена и labels — точно как в коде
(`instrumentation.go`, `bus.go`, `jetstream.go`, `conn.go`).

Meter и Tracer: `git.stit.tech/stit-core/golibinfra/natskit` (через
`internal/otelkit`). Инструменты создаются в `newNatsInstruments(instance, transport)` (свой
набор у `natsBus` и у `jsBus`; SDK склеивает их по имени в один инструмент);
невалидное имя инструмента — баг кита и паника на старте (`otelkit.Must`).
Без зарегистрированного `MeterProvider`/`TracerProvider` сигналы — no-op.

## Метрики

| Метрика | Тип | Labels | Когда / что |
|---|---|---|---|
| `nats_messages_published_total` | counter (`Int64Counter`) | `instance`, `status`, `subject`**, `transport`* | +1 на каждую публикацию. `status` ∈ `ok` / `error`. |
| `nats_publish_duration_seconds` | histogram (`Float64Histogram`, unit `s`) | `instance`, `subject`**, `transport`* | Длительность публикации (Core — вокруг `PublishMsg`; JetStream — до `PubAck`/ошибки/таймаута). Пишется и при `error`. |
| `nats_messages_consumed_total` | counter (`Int64Counter`) | `instance`, `subject`, `status`, `transport`* | +1 на каждое обработанное входящее сообщение. `status` ∈ `ok` / `handler_error` / `panic` / `dead` / `dlq_error`. |
| `nats_handle_duration_seconds` | histogram (`Float64Histogram`, unit `s`) | `instance`, `subject`, `transport`* | Длительность работы обработчика (включая ack/nak). Пишется при любом исходе. |
| `nats_inflight_messages` | up-down counter (`Int64UpDownCounter`) | `instance`, `subject`, `transport`* | Сообщения в обработке прямо сейчас. +1 на входе в обработчик, −1 на выходе. |

\* Label `transport` присутствует **только у JetStream-пути** (`jsBus`) со
значением `jetstream`. Core-путь (`natsBus`) этот label не ставит — для Core
серии без `transport`.

\*\* На publish-метриках label `subject` появляется, только если публикация
получила `natskit.WithMetricSubject(pattern)` (или `StreamMsg.MetricSubject`).
Без него label отсутствует: реальный subject публикации (`alerts.employee.<id>`)
завёл бы серию на каждый id.

Границы бакетов гистограмм `_seconds` — общие для всех китов
(`internal/otelkit.DurationBuckets`): `.005 .01 .025 .05 .1 .25 .5 1 2.5 5 10 30`.

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
| `dead` | consume (только JetStream) | Сообщение больше не ретраится: обработчик вернул ошибку, обёрнутую `natskit.ErrTerminal` (включая ошибку декода в `Subscribe*JSON`), **или** исчерпан `MaxDeliver`. DLQ-копия (если `NATS_JS_DLQ_SUBJECT` задан) сохранена, оригинал получил `Term`. |
| `dlq_error` | consume (только JetStream) | Сообщение мёртвое, но DLQ-публикация не удалась: `Term` не делается, оригинал возвращается на ределивери (`Nak`). Серия > 0 — DLQ недоступен. |

### Семантика label `subject`

- publish — шаблон из `WithMetricSubject` (`alerts.employee.*`); без опции label
  не ставится вовсе.
- consume (Core и JetStream) — **subject подписки** (`Subscribe(subject)` /
  `FilterSubject` консьюмера), не реальный subject сообщения. Wildcard-подписка
  `orders.*.created` даёт одну серию, а не по серии на каждый id. Реальный subject
  обработчик читает из `SubjectFromContext(ctx)`; в спанах он есть.

Кардинальность серий = число подписок × инстансов.

## Спаны

Кит открывает собственные спаны на обоих концах — это включает trace-propagation
через персист JetStream.

| Спан | Kind | Имя | Атрибуты |
|---|---|---|---|
| Producer | `SpanKindProducer` | `natskit.publish <шаблон или subject>` | `messaging.system=nats`, `messaging.destination.name=<subject>`, `messaging.operation.type=send`, `messaging.message.body.size`, `instance`; при `WithMetricSubject` — ещё `messaging.destination.template` |
| Consumer | `SpanKindConsumer` | `natskit.handle <фильтр подписки>` | `messaging.system=nats`, `messaging.destination.name` = реальный subject, `messaging.destination.template=<фильтр подписки>`, `messaging.operation.type=process`, `messaging.message.body.size`, `instance` |

Имя consumer-спана — фильтр подписки, а не реальный subject: для
`orders.*.created` иначе получилось бы имя на каждый id. Конкретный subject
остаётся в `messaging.destination.name`.

(Семантические соглашения — `semconv/v1.41.0`.)

- **Producer-спан** открывается в `startPublishSpan`, после чего W3C
  trace-context (`traceparent`/`tracestate`) инжектится в `nats.Header`
  публикуемого сообщения.
- **Consumer-спан** открывается в `startConsumerSpan`: контекст экстрактится из
  header'ов входящего сообщения, спан стартует как child producer-спана. Так
  трасса остаётся связной даже через персист JetStream. `ctx` обработчика —
  child этого спана; в JetStream он несёт deadline `HandlerTimeout`.
- Метрики пишутся с тем же `ctx`, что несёт спан измерения (publish — ctx
  producer-спана, consume — ctx обработчика): при включённых exemplar'ах из
  гистограммы `nats_handle_duration_seconds` можно прыгнуть в конкретный трейс.
- На обоих концах при ошибке/панике вызывается `span.RecordError` +
  `span.SetStatus(codes.Error, …)`.

## Логи

Все записи — через `logkit.Logger` инстанса: `Conn`, `Bus` и `StreamBus`
добавляют к логгеру поле `instance` (`log.With`), поэтому оно есть в каждой
записи кита. Userinfo в поле `url` редактируется (`nats://xxxxx@host:4222`) —
пароль и токен из `NATS_URL` в логи не попадают.

| Уровень | Сообщение | Когда |
|---|---|---|
| `error` | `natskit: async error` | Асинхронная ошибка клиента nats.go: slow consumer (поля `subject`, `dropped` — сколько сообщений потеряно), сбой drain, ошибки протокола. |
| `warn` | `natskit: disconnected` | Разрыв соединения (не при `Close`). Поле `err` — причина, если есть. |
| `info` | `natskit: reconnected` | Переподключение; поле `url` (userinfo скрыт). |
| `error` | `natskit: connection closed, reconnects exhausted` | `NATS_MAX_RECONNECTS` исчерпан: соединение закрыто навсегда, подписки мертвы. Поля `url`, `err` (`LastError`). |
| `info` | `natskit: dlq subject is stored in a stream` | На старте: DLQ-subject накрыт стримом `stream`, копии уйдут JetStream-публикацией с `PubAck`. |
| `warn` | `natskit: dlq stream lookup` | Не удалось выяснить, накрыт ли DLQ-subject стримом; копии уйдут Core-публикацией. |
| `warn` | `natskit: stream config drift` | Конфиг существующего стрима расходится с ENV (поля `stream`, `field`, `want`, `have`); кит стрим не переписывает. |
| `warn` | `natskit: subscription drain` | Подписка не додренилась в бюджет `Close` / ошибка снятия интереса. |
| `warn` | `natskit: drain timeout, handlers still in flight` | `Close` исчерпал `DrainTimeout`, обработчики ещё работают (поле `inflight`). |
| `warn` | `natskit: drain timeout, closing connection` | Flush публикаций не уложился в остаток бюджета — соединение закрыто принудительно. |
| `warn` | `natskit: jetstream consume error` | Ошибка pull-консьюмера (нет heartbeat, консьюмер удалён, соединение закрыто); поля `stream`, `subject`, `durable`. |
| `warn` | `natskit: jetstream in-progress` | Не удалось отправить `InProgress` — сервер может переотправить сообщение раньше, чем обработчик закончит. |
| `error` | `natskit: handler error` / `natskit: jetstream handler error` | Обработчик вернул ошибку (кроме `context.Canceled`). |
| `error` | `natskit: handler panic` / `natskit: jetstream handler panic` | Паника обработчика, со стеком. |
| `warn` | `natskit: jetstream nak error` | Не удалось вернуть сообщение на ретрай; сервер переотправит его после `AckWait`. |
| `error` | `natskit: jetstream message dead` | Сообщение больше не ретраится (DLQ-копия сохранена, оригинал терминирован): поля `stream`, `durable`, `subject`, `deliveries`, `reason` (`terminal`/`max-deliver`), `dlq`, `err` — причина последней попытки. |
| `error` | `natskit: jetstream dlq publish failed, message kept for redelivery` | DLQ-публикация мёртвого сообщения не удалась: `Term` не сделан, оригинал ушёл на ределивери. Поля те же плюс `err` — ошибка публикации, `cause` — причина смерти. |
| `error` | `natskit: jetstream ack error` / `… term error` | Сбой подтверждения сообщения. |

## Health-probe

`natskit.Probe(client) healthkit.Probe` (имя пробы — `natskit/<instance>`)
проверяет `Client.Healthy()`: `nil` при статусе `nats.CONNECTED`,
`natskit.ErrConnectionClosed` (с `LastError`) после окончательного закрытия
соединения и ошибка со статусом (`RECONNECTING`, `DRAINING`) в промежуточных
состояниях. `Module` пробу **не** provide'ит автоматически: в readiness
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
