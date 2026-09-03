# Использование natskit

Рецепты по `Bus` (Core NATS) и `StreamBus` (JetStream). Все сигнатуры — точно
как в коде (`bus.go`, `jetstream.go`, `subscription.go`, `context.go`).

## Сигнатуры API

### Bus (Core NATS)

```go
type Handler func(ctx context.Context, payload []byte) error

type Bus interface {
	Publish(ctx context.Context, subject string, payload any) error
	Subscribe(ctx context.Context, subject string, handler Handler) (Subscription, error)
	SubscribeQueue(ctx context.Context, subject, queue string, handler Handler) (Subscription, error)
}

func NewBus(conn *Conn, log logkit.Logger, cfg Config) Bus // низкоуровнево; обычный путь — Client.Bus()

// Дженерики: marshal/unmarshal JSON за вызывающего.
func PublishJSON[T any](b Bus, ctx context.Context, subject string, msg T) error
func SubscribeJSON[T any](b Bus, ctx context.Context, subject string, h func(ctx context.Context, msg T) error) (Subscription, error)
func SubscribeQueueJSON[T any](b Bus, ctx context.Context, subject, queue string, h func(ctx context.Context, msg T) error) (Subscription, error)
```

### StreamBus (JetStream)

```go
type StreamBus interface {
	PublishStream(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error
	PublishStreamBatch(ctx context.Context, msgs []StreamMsg) (results []error, err error)
	SubscribeStream(ctx context.Context, stream, subject, durable string, handler Handler) (Subscription, error)
}

func NewStreamBus(conn *Conn, log logkit.Logger, cfg Config) (StreamBus, error)

type StreamMsg struct {
	Subject string
	Payload []byte
	MsgID   string // → Nats-Msg-Id (дедуп в окне Duplicates)
}

func WithMsgID(id string) PublishOption

// Дженерики.
func PublishStreamJSON[T any](b StreamBus, ctx context.Context, subject string, msg T) error
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
func (c *Client) Close()                 // graceful: drain + ожидание обработчиков в пределах DrainTimeout
func Probe(c *Client) healthkit.Probe

func NewConn(cfg Config, log logkit.Logger) (*Conn, error)
var ErrClosed error                      // Subscribe после начала Close
```

### Контекст обработчика

```go
func SubjectFromContext(ctx context.Context) string // субъект входящего сообщения; "" вне обработчика
func HeadersFromContext(ctx context.Context) nats.Header // заголовки входящего; nil вне обработчика
```

> Порядок аргументов дженериков необычен — `(b, ctx, ...)`, а не `(ctx, b, ...)`.
> Это вынужденно: Go не выводит тип-параметр `T` из метода интерфейса, поэтому
> bus идёт первым аргументом свободной функции.

## Подключение через fx

`natskit.Module(name)` поднимает инстанс шины и provide'ит его `Config`,
`*Client`, `Bus` и `StreamBus`. Пустое имя — дефолтный инстанс: конфиг из
`NATS_*`, provide без тега.

```go
fx.New(
	logkit.Module,
	natskit.Module(""),
	natskit.HealthProbe(""), // опц. проба инстанса в group:"health" (= healthkit.Provide(natskit.Probe))
	fx.Invoke(func(bus natskit.Bus, sb natskit.StreamBus) error {
		// регистрация подписчиков на старте
		_, err := natskit.SubscribeJSON(bus, context.Background(), "orders.created", handleOrder)
		return err
	}),
)
```

Подписки регистрируйте в `fx.Invoke` или в `OnStart`-хуке — до этого граф не
собран. `Subscribe`/`SubscribeStream` неблокирующие: ставят callback и сразу
возвращают `Subscription`, обработка идёт в фоновых горутинах. `ctx` в них
ограничивает только регистрацию (вызовы JetStream API); подписка живёт до
`Subscription.Unsubscribe`/`Drain` или `Close` инстанса — `OnStop`-хук `Module`
делает это сам, handle можно не хранить.

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
	natskit.Module(""),      // своя шина:    NATS_URL, NATS_JS_STREAM_NAMES, …
	natskit.Module("hub"),   // внешняя шина: <NAME>_NATS_* с NAME=HUB
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
cfg, err := natskit.NewConfigPrefixed("HUB")
client, err := natskit.New("hub", cfg, log)
defer client.Close()

err = client.Stream().PublishStream(ctx, "alerts.employee."+id, payload, natskit.WithMsgID(eventID))
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

## DLQ / NAK

Poison-обработка настраивается через ENV (`NATS_JS_MAX_DELIVER`,
`NATS_JS_NAK_BACKOFF`, `NATS_JS_DLQ_SUBJECT` — см.
[configuration.md](configuration.md)), не в коде. Обработчик просто возвращает
ошибку — остальное делает кит.

DLQ-сообщение приходит как обычное сообщение в `DLQSubject` — Core-подписчику
или, если subject накрыт стримом, JetStream-консьюмеру. Метаданные — в
заголовках (константы `HeaderDLQOrigin`/`HeaderDLQError`/`HeaderDLQMsgID`),
читаемых через `HeadersFromContext`:

```go
sub, err := bus.Subscribe(ctx, "dlq.orders", func(ctx context.Context, payload []byte) error {
	h := natskit.HeadersFromContext(ctx)
	origin := h.Get(natskit.HeaderDLQOrigin) // исходный subject
	cause := h.Get(natskit.HeaderDLQError)   // текст ошибки обработчика
	msgID := h.Get(natskit.HeaderDLQMsgID)   // исходный Nats-Msg-Id, если был
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

`HeadersFromContext(ctx)` даёт заголовки сообщения (`event-type`,
`schema-version` и т.п.). Там же лежит W3C trace-context — это нормально,
читайте только нужные ключи.

## Graceful shutdown

`Client.Close()` (его вызывает `OnStop`-хук `Module`) дренит подписки, ждёт
работающие обработчики и только потом закрывает соединение — обработчики
успевают `Ack`. Бюджет — `NATS_DRAIN_TIMEOUT` (10s по умолчанию); держите его
меньше stop-таймаута fx/оркестратора. Обработчики, не уложившиеся в бюджет,
теряют соединение: JetStream переотправит их сообщения после `AckWait`.

## Обработка ошибок и паник

- Обработчик вернул ошибку: Core логирует (кроме `context.Canceled`), сообщение
  уже сконсюмлено; JetStream делает `Nak`/`NakWithDelay` (ретрай).
- Обработчик паникнул: recover ловит, логирует стек, подписчик продолжает жить.
  Core теряет сообщение; JetStream идёт по poison-пути.
- Публикация JSON: ошибка marshal'а возвращается из `PublishJSON`/
  `PublishStreamJSON` (`natskit: marshal: …`). Ошибка unmarshal'а в подписчике
  возвращается как handler-ошибка (`natskit: unmarshal: …`).
- Подписка после начала `Close` — `ErrClosed`.
