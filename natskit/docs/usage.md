# Использование natskit

Рецепты по `Bus` (Core NATS) и `StreamBus` (JetStream). Все сигнатуры — точно
как в коде (`bus.go`, `jetstream.go`, `subscription.go`, `context.go`).

## Сигнатуры API

### Bus (Core NATS)

```go
type Handler func(ctx context.Context, payload []byte) error

type Bus interface {
	Publish(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error
	Subscribe(ctx context.Context, subject string, handler Handler) (Subscription, error)
	SubscribeQueue(ctx context.Context, subject, queue string, handler Handler) (Subscription, error)
}

func NewBus(conn *Conn, log logkit.Logger, cfg Config) Bus // низкоуровнево; обычный путь — Client.Bus()

// Дженерики: marshal/unmarshal JSON за вызывающего.
func PublishJSON[T any](b Bus, ctx context.Context, subject string, msg T, opts ...PublishOption) error
func SubscribeJSON[T any](b Bus, ctx context.Context, subject string, h func(ctx context.Context, msg T) error) (Subscription, error)
func SubscribeQueueJSON[T any](b Bus, ctx context.Context, subject, queue string, h func(ctx context.Context, msg T) error) (Subscription, error)
```

`Publish` принимает готовые байты — `Bus` и `StreamBus` симметричны, а JSON
добавляют дженерики `PublishJSON`/`PublishStreamJSON`.

### Опции публикации

```go
func WithMsgID(id string) PublishOption            // Nats-Msg-Id: дедуп стримом в окне Duplicates
func WithHeader(key, value string) PublishOption   // прикладной заголовок (event-type, schema-version…)
func WithMetricSubject(pattern string) PublishOption // значение label `subject` publish-метрик

// Разобранные опции — для фейков шины вне кита (testkit/natstest).
type PublishSpec struct {
	MsgID         string
	Header        nats.Header
	MetricSubject string
}

func NewPublishSpec(opts ...PublishOption) PublishSpec
func (s PublishSpec) Headers() nats.Header
```

### StreamBus (JetStream)

```go
type StreamBus interface {
	PublishStream(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error
	PublishStreamBatch(ctx context.Context, msgs []StreamMsg) (results []error, err error)
	SubscribeStream(ctx context.Context, stream, subject, durable string, handler Handler) (Subscription, error)
}

func NewStreamBus(conn *Conn, log logkit.Logger, cfg Config) (StreamBus, error)

// Проверка стримов публикатора на старте; реализует StreamBus из New/Module, фейки — нет.
type StreamChecker interface {
	CheckStreams(ctx context.Context, filters ...string) error
}
var ErrJetStreamDisabled error // NATS_JETSTREAM_ENABLED=false — любой метод; сервер без JetStream — CheckStreams
var ErrNoStream error          // CheckStreams: фильтр не накрыт целиком ни одним стримом

type StreamMsg struct {
	Subject       string
	Payload       []byte
	MsgID         string      // → Nats-Msg-Id (дедуп в окне Duplicates)
	Header        nats.Header // прикладные заголовки (аналог WithHeader)
	MetricSubject string      // label `subject` publish-метрик (аналог WithMetricSubject)
}

// Дженерики.
func PublishStreamJSON[T any](b StreamBus, ctx context.Context, subject string, msg T, opts ...PublishOption) error
func SubscribeStreamJSON[T any](b StreamBus, ctx context.Context, stream, subject, durable string, h func(ctx context.Context, msg T) error) (Subscription, error)
```

### Subscription

```go
type Subscription interface {
	Unsubscribe() error            // снять интерес сразу; обработчики не ждутся
	Drain(ctx context.Context) error // снять интерес, дообработать буфер, дождаться обработчиков (в пределах ctx)
}
```

### Клиент и соединение

```go
func New(name string, cfg Config, log logkit.Logger) (*Client, error)
func (c *Client) Bus() Bus
func (c *Client) Stream() StreamBus
func (c *Client) Close()                          // graceful: drain + ожидание обработчиков в пределах DrainTimeout
func (c *Client) CloseContext(ctx context.Context) // бюджет min(DrainTimeout, дедлайн ctx)
func (c *Client) Healthy() error
func Probe(c *Client) healthkit.Probe

func NewConn(cfg Config, log logkit.Logger) (*Conn, error)
var ErrClosed error           // Subscribe после начала Close
var ErrConnectionClosed error // соединение закрыто окончательно (Healthy/Probe)
var ErrConfig error           // невалидный конфиг (NewConfig/Validate/New)
var ErrTerminal error         // маркер «не ретраить» для обработчика
```

### Контекст обработчика

```go
func SubjectFromContext(ctx context.Context) string // субъект входящего сообщения; "" вне обработчика
func HeadersFromContext(ctx context.Context) nats.Header // заголовки входящего; nil вне обработчика

// Для фейков шины: кладёт subject и заголовки так, как это делает кит.
func ContextWithMessage(ctx context.Context, subject string, h nats.Header) context.Context

// Накрывает ли фильтр (с * и >) subject — та же семантика, что у подписки; subject-фильтр
// накрыт, только если накрыт каждый подходящий под него subject.
func SubjectMatches(filter, subject string) bool
```

> Порядок аргументов дженериков необычен — `(b, ctx, ...)`, а не `(ctx, b, ...)`.
> Это вынужденно: Go не выводит тип-параметр `T` из метода интерфейса, поэтому
> bus идёт первым аргументом свободной функции.

## Подключение через fx

`natskit.Module(name)` поднимает инстанс шины и provide'ит `*Client`, `Bus` и
`StreamBus`. Конфиг регистрирует приложение: `fx.Provide(natskit.NewConfig)` для
дефолтного инстанса, `natskit.ProvideConfig(name)` — для именованного.

```go
fx.New(
	logkit.Module,
	fx.Provide(logkit.NewConfig, natskit.NewConfig),
	natskit.Module(""),
	natskit.HealthProbe(""), // опц. проба инстанса в группе healthkit.Group (= healthkit.Provide(natskit.Probe))
	fx.Invoke(func(lc fx.Lifecycle, sb natskit.StreamBus) {
		natskit.SubscribeOnStart(lc, func(ctx context.Context) (natskit.Subscription, error) {
			return natskit.SubscribeStreamJSON(sb, ctx, "ORDERS", "orders.created", "billing", handleOrder)
		})
	}),
)
```

Подменить конфиг (тесты, значения из App-структуры) — обычный `fx.Provide`
своего конструктора `natskit.Config` вместо `natskit.NewConfig`.

Подписку на жизненный цикл fx вешает `natskit.SubscribeOnStart(lc, subscribe)`:

```go
func SubscribeOnStart(lc fx.Lifecycle, subscribe func(ctx context.Context) (Subscription, error))
```

- `subscribe` вызывается в `OnStart` — после старта зависимостей, объявленных
  раньше подписчика (пул БД, миграции), с `ctx` хука: он ограничивает только
  регистрацию.
- Ошибка подписки валит старт — в том числе `ErrJetStreamDisabled`: подписчик
  при `NATS_JETSTREAM_ENABLED=false` — ошибка конфигурации, а не молча
  выключенный приём.
- `OnStop` дренит подписку в пределах stop-ctx до остановки этих зависимостей:
  обработчик в работе не упирается в закрытый пул.
- Зовите его из `fx.Invoke` или конструктора, а не из чужого `OnStart`: хук,
  добавленный во время старта, fx не запускает — подписки не будет, ошибки тоже.

`Subscribe`/`SubscribeStream` неблокирующие: ставят callback и сразу возвращают
`Subscription`, обработка идёт в фоновых горутинах. `ctx` в них ограничивает
только регистрацию (вызовы JetStream API); подписка живёт до
`Subscription.Unsubscribe`/`Drain` или `Close` инстанса — `OnStop`-хук `Module`
дренит оставшиеся подписки сам.

## Остановить одну подписку

```go
sub, err := natskit.SubscribeJSON(bus, ctx, "orders.created", handleOrder)
…
// переключение режима: снять интерес, дождаться уже идущих обработчиков
drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
if err := sub.Drain(drainCtx); err != nil { … } // ctx истёк — обработчики ещё работают

// немедленно, без ожидания
_ = sub.Unsubscribe()
```

Для JetStream `Unsubscribe` останавливает pull-консьюмер на стороне клиента;
durable-консьюмер на сервере остаётся, и не подтверждённые сообщения уйдут на
ределивери после `AckWait`.

## Несколько шин в одном приложении

Сервису, у которого есть своя шина и внешняя (шина-интегратор соседнего
продукта), нужны два независимых инстанса. Именованный инстанс читает
`<NAME>_NATS_*` и provide'ится под fx-тегом `name:"<name>"`:

```go
fx.New(
	logkit.Module,
	fx.Provide(logkit.NewConfig, natskit.NewConfig),
	natskit.Module(""),            // своя шина:    NATS_URL, NATS_JS_STREAM_NAMES, …
	natskit.ProvideConfig("hub"),  // HUB_NATS_* под тегом name:"hub"
	natskit.Module("hub"),         // внешняя шина
	fx.Provide(fx.Annotate(NewAlertPublisher, fx.ParamTags(`name:"hub"`))),
)

func NewAlertPublisher(hub natskit.StreamBus) *AlertPublisher { … }
```

Переменные инстансов изолированы: `<NAME>_NATS_JS_STREAM_NAMES` не задан —
значит список пуст, а не унаследован из `NATS_JS_STREAM_NAMES`. Это защищает от
публикации в чужую шину и от создания там чужих стримов: инстанс, который только
публикует в стрим под управлением соседней команды, оставляет
`<NAME>_NATS_JS_STREAM_NAMES` незаданным (или ставит
`<NAME>_NATS_JS_STREAM_MANAGED=false`, если хочет проверять наличие стрима).

Имя инстанса едет атрибутом `instance` в метриках, спанах и логах (см.
[observability.md](observability.md)), поэтому сигналы двух шин не сливаются.

Без fx инстанс собирается напрямую:

```go
cfg, err := natskit.NewConfigPrefixed("hub")
client, err := natskit.New("hub", cfg, log)
defer client.Close()

// WithMetricSubject задаёт шаблон для label `subject`: без него per-id subject
// завёл бы серию метрик на каждого сотрудника.
err = client.Stream().PublishStream(ctx, "alerts.employee."+id, payload,
	natskit.WithMsgID(eventID), natskit.WithMetricSubject("alerts.employee.*"))
```

## Core NATS: типобезопасный pub/sub

```go
type OrderCreated struct {
	ID int `json:"id"`
}

// Publisher.
err := natskit.PublishJSON(bus, ctx, "orders.created", OrderCreated{ID: 42})

// Subscriber (fan-out — каждому инстансу).
sub, err := natskit.SubscribeJSON(bus, ctx, "orders.created",
	func(ctx context.Context, m OrderCreated) error {
		return process(ctx, m) // вернёте ошибку → status=handler_error в метрике
	})
```

### Load-balanced подписка (queue-группа)

Несколько инстансов с одним именем queue-группы делят нагрузку — каждое
сообщение получает ровно один инстанс из группы:

```go
sub, err := natskit.SubscribeQueueJSON(bus, ctx, "jobs.process", "workers",
	func(ctx context.Context, j Job) error {
		return handle(ctx, j)
	})
```

`Subscribe` (fan-out) vs `SubscribeQueue` (один из группы) — выбирайте по тому,
должны ли событие обработать все инстансы или ровно один.

## JetStream: персист и at-least-once

```go
// Декларация стрима — через ENV: NATS_JETSTREAM_ENABLED=true,
// NATS_JS_STREAM_NAMES=ORDERS (subjects ORDERS.>).

// Publisher.
err := natskit.PublishStreamJSON(sb, ctx, "ORDERS.created", OrderCreated{ID: 7})

// Durable-подписчик: durable-имя держит позицию между переподключениями.
sub, err := natskit.SubscribeStreamJSON(sb, ctx, "ORDERS", "ORDERS.created", "orders-worker",
	func(ctx context.Context, m OrderCreated) error {
		// nil → Ack; ошибка → Nak/ретрай; исчерпание MaxDeliver → DLQ/Term.
		// ctx несёт deadline HandlerTimeout — передавайте его в БД/HTTP.
		return process(ctx, m)
	})
```

`SubscribeStream(stream, subject, durable, ...)`: `subject` — `FilterSubject`
консьюмера (может быть wildcard, напр. `ORDERS.>`); `durable` — имя
durable-консьюмера (инстансы с одним именем делят нагрузку).

### Медленные обработчики

Пока обработчик работает, кит каждые `AckWait/2` шлёт серверу `InProgress` —
сообщение не переотправляется другому инстансу. Бюджет одной попытки задаёт
`HandlerTimeout` (по умолчанию = `AckWait`): по его истечении `ctx` обработчика
отменяется, обработчик обязан вернуть ошибку (`ctx.Err()`), и сообщение уходит
на ретрай. Тяжёлые задачи (минуты) — поднимайте `NATS_JS_HANDLER_TIMEOUT`, не
`NATS_JS_ACK_WAIT`: второй отвечает за скорость failover при смерти процесса
(см. [configuration.md](configuration.md#ackwait-и-handlertimeout)).

### Дедуп публикации (`Nats-Msg-Id`)

```go
// Повтор с тем же MsgID в окне Duplicates стрим не сохранит дважды.
err := sb.PublishStream(ctx, "ORDERS.created", payload, natskit.WithMsgID("order-42"))
```

### Батч-публикация

`PublishStreamBatch` шлёт все сообщения async и ждёт `PubAck` по каждому:

```go
batch := []natskit.StreamMsg{
	{Subject: "ORDERS.created", Payload: p1, MsgID: "order-1"},
	{Subject: "ORDERS.created", Payload: p2, MsgID: "order-2"},
}
results, err := sb.PublishStreamBatch(ctx, batch)
// err != nil       — сбой всего батча (JS выключен / отмена ctx)
// results[i] == nil — i-е сообщение подтверждено персистом (дубль по MsgID — тоже PubAck, не ошибка)
// results[i] != nil — i-е не опубликовано; потерянный PubAck — jetstream.ErrAsyncPublishTimeout
if err != nil {
	return err
}
for i, e := range results {
	if e != nil {
		// сообщение i не сохранилось — на ретрай
	}
}
```

Батчи одной шины независимы: метод ждёт только свои `PubAck`'и, поэтому
потерянный ack соседнего батча (например, второго outbox-инстанса на том же
`Client`) не задерживает этот. Общий потолок ожидания на сообщение —
`NATS_JS_PUBLISH_ASYNC_TIMEOUT`; отмена `ctx` возвращает исход по каждому
сообщению (уже подтверждённые — успех) и ошибку батча.

### Проверка стримов публикатора

Публикация в subject без стрима не получает `PubAck` (`jetstream.ErrNoStreamResponse`).
Публикатор узнаёт об этом на старте, а не на первом событии:

```go
if checker, ok := sb.(natskit.StreamChecker); ok { // фейки шины стримов не знают
	return checker.CheckStreams(ctx, "ORDERS.>") // ErrJetStreamDisabled или ErrNoStream
}
```

Фильтр (subject или фильтр с `*`, `>`) накрыт, если subject одного стрима накрывает каждый
подходящий под него subject: пересечения мало — стрим `ORDERS.created` фильтр `ORDERS.>` не
накрывает. Сначала метод спрашивает у сервера информацию аккаунта: сервер без JetStream на
публикацию отвечает «нет ответа от стрима», как subject без стрима, и только так их различить;
без фильтров проверка этим и ограничена. `outboxkit.Module` и `outboxkit.Instance` зовут его в
`OnStart`, транспорт outbox — на «нет ответа от стрима».

## DLQ / NAK

Poison-обработка настраивается через ENV (`NATS_JS_MAX_DELIVER`,
`NATS_JS_NAK_BACKOFF`, `NATS_JS_NAK_MAX_BACKOFF`, `NATS_JS_DLQ_SUBJECT` — см.
[configuration.md](configuration.md)), не в коде. Обработчик возвращает ошибку и
не логирует её — остальное делает кит: `error`-запись с `subject` и `trace_id`,
`Nak` с растущей задержкой (при дефолтах ретраи идут 15 минут), по исчерпании —
DLQ и `Term`.

Ошибку, которую ретраить бессмысленно (неизвестная схема, чужой тип события),
обработчик помечает `natskit.ErrTerminal` — сообщение уходит в DLQ и получает
`Term` без ретраев:

```go
sub, err := natskit.SubscribeStreamJSON(sb, ctx, "ORDERS", "ORDERS.created", "orders-worker",
	func(ctx context.Context, m OrderCreated) error {
		if m.Version != 2 {
			return fmt.Errorf("%w: schema version %d", natskit.ErrTerminal, m.Version)
		}
		return process(ctx, m) // обычная ошибка → NAK и ретрай
	})
```

Ошибка декода JSON в `Subscribe*JSON` уже терминальна: битый payload не станет
валидным при ределивери.

`Term` кит делает только после успешной DLQ-копии: если публикация в
`NATS_JS_DLQ_SUBJECT` не удалась, сообщение остаётся в стриме и уходит на
ределивери (`error`-запись `natskit: jetstream dlq publish failed, message kept
for redelivery`, метрика со `status=dlq_error`).

DLQ-сообщение приходит как обычное сообщение в `DLQSubject` — Core-подписчику
или, если subject накрыт стримом, JetStream-консьюмеру с **другим** конфигом (в
нём `NATS_JS_DLQ_SUBJECT` не задан: фильтр, накрывающий собственный DLQ-subject,
кит отвергает). Метаданные — в заголовках, читаемых через `HeadersFromContext`:

```go
sub, err := bus.Subscribe(ctx, "dlq.orders", func(ctx context.Context, payload []byte) error {
	h := natskit.HeadersFromContext(ctx)
	origin := h.Get(natskit.HeaderDLQOrigin)     // исходный subject
	cause := h.Get(natskit.HeaderDLQError)       // текст ошибки последней попытки
	reason := h.Get(natskit.HeaderDLQReason)     // terminal | max-deliver
	stream := h.Get(natskit.HeaderDLQStream)     // стрим и консьюмер, где сообщение умерло
	consumer := h.Get(natskit.HeaderDLQConsumer)
	tries := h.Get(natskit.HeaderDLQDeliveries)  // сколько раз доставлялось
	msgID := h.Get(natskit.HeaderDLQMsgID)       // исходный Nats-Msg-Id, если был
	// разбор/алерт/ручной retry
	return nil
})
```

## Subject и headers внутри обработчика

Для wildcard-подписки конкретный субъект доступен из контекста — не нужно
дублировать его в payload:

```go
sub, err := bus.Subscribe(ctx, "orders.*", func(ctx context.Context, payload []byte) error {
	switch natskit.SubjectFromContext(ctx) {
	case "orders.created":
		// ...
	case "orders.cancelled":
		// ...
	}
	return nil
})
```

`HeadersFromContext(ctx)` даёт заголовки сообщения. Публикатор ставит их
`WithHeader` (или `StreamMsg.Header`):

```go
err := natskit.PublishJSON(bus, ctx, "orders.created", m,
	natskit.WithHeader("Event-Type", "order.created"),
	natskit.WithHeader("Schema-Version", "2"))

sub, err := bus.Subscribe(ctx, "orders.>", func(ctx context.Context, payload []byte) error {
	switch natskit.HeadersFromContext(ctx).Get("Event-Type") {
	case "order.created":
		// ...
	}
	return nil
})
```

Регистр ключа сохраняется как при публикации. Там же лежит W3C trace-context —
это нормально, читайте только нужные ключи.

## Graceful shutdown

`Client.Close()` дренит подписки, ждёт работающие обработчики и только потом
закрывает соединение — обработчики успевают `Ack`. Бюджет — `NATS_DRAIN_TIMEOUT`
(10s по умолчанию). `OnStop`-хук `Module` вызывает `Client.CloseContext(ctx)`:
бюджет ужимается до дедлайна ctx, поэтому shutdown укладывается в `StopTimeout`
приложения. Обработчики, не уложившиеся в бюджет, получают отмену `ctx` и теряют
соединение: JetStream переотправит их сообщения после `AckWait`.

## Обработка ошибок и паник

- Обработчик вернул ошибку: кит пишет `error`-запись `natskit: handler error` /
  `natskit: jetstream handler error` с `subject`, `err` и `trace_id` (кроме
  `context.Canceled`) — обработчик ту же ошибку в лог не пишет. Core: сообщение
  уже сконсюмлено; JetStream: `NakWithDelay` с растущей задержкой (ретрай).
- Обработчик вернул ошибку, обёрнутую `natskit.ErrTerminal`: JetStream — DLQ +
  `Term` без ретраев; Core — обычная ошибка обработчика.
- Обработчик паникнул: recover ловит, логирует стек, подписчик продолжает жить.
  Core теряет сообщение; JetStream идёт по poison-пути.
- Обработчик не уложился в `NATS_HANDLER_TIMEOUT` (Core) или
  `NATS_JS_HANDLER_TIMEOUT` (JetStream): `ctx` отменяется, ошибка обрабатывается
  как обычная.
- Публикация JSON: ошибка marshal'а возвращается из `PublishJSON`/
  `PublishStreamJSON` (`natskit: marshal payload: …`). Ошибка unmarshal'а в
  подписчике возвращается как терминальная handler-ошибка
  (`natskit: terminal: unmarshal payload: …`).
- Подписка после начала `Close` — `ErrClosed`.
