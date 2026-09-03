# outboxkit — рецепты

Сигнатуры приведены как в коде; импорты опущены ради краткости.

## Single-instance: fx Module

Одна таблица `outbox`, проводка через fx. `pgstore.Module` даёт `Store` как
`outboxkit.Repository`, регистрирует миграцию кита в `pgkit.MigrateModule`,
создаёт партиции в `OnStart` и держит partition/retention jobs;
`outboxkit.Module` — `Bus`/`Publisher`/`Worker`/drain-`Job`. Конфиги
(`outboxkit.NewConfig`, `pgstore.NewConfig`) caller провайдит явно.

```go
fx.New(
    pgkit.Module,        // *pgkit.DB, pgkit.Transactor
    pgkit.MigrateModule, // применяет pgstore.Migrations до OnStart партиций
    natskit.Module(""),  // natskit.StreamBus (NATS_JETSTREAM_ENABLED=true + стрим)
    timerkit.Module,     // гоняет Worker.Run и keeper-jobs по тикерам

    fx.Provide(pgstore.NewConfig, outboxkit.NewConfig),
    pgstore.Module,   // Store→Repository, миграции, партиции, retention
    outboxkit.Module, // Bus, Publisher, Worker, drain-Job

    fx.Invoke(func(p outboxkit.Publisher) {
        // передать Publisher в доменный сервис
    }),
)
```

`pgkit.MigrateModule` в списке стоит раньше `pgstore.Module`: `OnStart`-хуки
выполняются в порядке регистрации, а партиции создаются поверх таблицы из
миграции. tx-strict guard включается `OUTBOX_REQUIRE_TX=true`.

## Публикация в транзакции

`Publisher.Publish` вызывается внутри той же транзакции, что и доменная запись —
в этом весь смысл паттерна (атомарность факта и его публикации):

```go
err := transactor.WithTx(ctx, func(ctx context.Context) error {
    if err := orders.Insert(ctx, order); err != nil {
        return err
    }
    return publisher.Publish(ctx, "order.created", OrderCreated{ID: order.ID})
})
```

`payload` — любое значение, сериализуемое `json.Marshal`.

## Multi-instance: New + NewStoreFor + NewStreamTransportFor

Несколько независимых outbox'ов (таблица на домен) в одном приложении.
Собирается вручную, потому что каждый инстанс — своя таблица и свой namespace для
`Nats-Msg-Id`.

```go
// сигнатуры
func pgstore.NewStoreFor(db *pgkit.DB, table string, opts ...pgstore.StoreOption) (*pgstore.Store, error)
func outboxkit.NewStreamTransportFor(sb natskit.StreamBus, namespace string) outboxkit.Transport
func outboxkit.New(name string, repo outboxkit.Repository, tr outboxkit.Transport, tx pgkit.Transactor, cfg outboxkit.Config) *outboxkit.Outbox
```

```go
// на каждый домен — своя таблица + namespace'ный транспорт
ordersStore, err := pgstore.NewStoreFor(db, "outbox_orders", pgstore.WithRequireTx())
if err != nil {
    return err
}
ordersTr := outboxkit.NewStreamTransportFor(sb, "orders") // Msg-Id = "orders:<id>"
orders := outboxkit.New("orders", ordersStore, ordersTr, transactor, cfg)

leadsStore, _ := pgstore.NewStoreFor(db, "outbox_leads")
leadsTr := outboxkit.NewStreamTransportFor(sb, "leads")    // Msg-Id = "leads:<id>"
leads := outboxkit.New("leads", leadsStore, leadsTr, transactor, cfg)
```

namespace обязателен, если таблицы делят один JetStream-стрим: per-domain
`BIGSERIAL` независимы, и голый id столкнулся бы (см.
[architecture.md](architecture.md) § Namespaced Nats-Msg-Id). `name` инстанса
становится label'ом `instance` в метриках.

Проводка инстансов: `Outbox.Publisher()` — туда, где нужна доменная публикация;
`Outbox.Job()` (`timerkit.Job` с интервалом из конфига) — в `timer.job`-группу.
Схему и партиции кастомных таблиц заводит caller — `pgstore.SchemaSQL(table)` в
своей миграции, `Store.EnsurePartitions(ctx, 1)` в `OnStart` и по расписанию
(`pgstore.PartitionKeeperJob(store)`); retention — `Store.DropOldPartitions(ctx,
keepMonths)` (`pgstore.RetentionKeeperJob(store, keepMonths)`).

## MarkPublished и drain вручную

Обычно `Worker.Run` (через `timerkit.Job`) делает весь цикл list → publish → mark
в одной транзакции. Прямые методы `Repository` доступны, если нужен кастомный
drain:

```go
func (s *pgstore.Store) ListUnpublished(ctx context.Context, limit int, mode outboxkit.PublishMode) ([]outboxkit.Message, error)
func (s *pgstore.Store) MarkPublished(ctx context.Context, ids []int64) error
func (s *pgstore.Store) RecordFailures(ctx context.Context, failures []outboxkit.Failure) error
```

`ListUnpublished` берёт только строки с `published_at IS NULL AND dead_at IS NULL`,
сортирует по `created_at, id` и запирает их `FOR UPDATE` до конца транзакции
вызывающего — поэтому вызывается внутри `pgkit.Transactor.WithTx`. В `batch`
занятые другими строки пропускаются (`SKIP LOCKED`), в `ordered` занятая голова
очереди — `outboxkit.ErrBusy` (`NOWAIT`). `MarkPublished` — один batch-`UPDATE`
по `id = ANY($1)` с `published_at = NOW()`. `RecordFailures` одним `UPDATE` через
`unnest` инкрементит `attempts`, пишет `last_error` и проставляет `dead_at` тем, у
кого `Failure.Dead = true`.

## BacklogReporter (lag / pending)

`pgstore.Store` реализует `outboxkit.BacklogReporter`. Single-instance `Module`
подключает gauge'и автоматически. Вручную:

```go
func outboxkit.RegisterBacklogGauges(instance string, reporter outboxkit.BacklogReporter) (metric.Registration, error)

reg, err := outboxkit.RegisterBacklogGauges("orders", ordersStore)
if err != nil {
    return err
}
// на shutdown:
defer reg.Unregister()
```

Это включает `outbox_lag_seconds{instance,subject}` и
`outbox_pending_count{instance,subject}` (см.
[observability.md](observability.md)). `Store.Backlog(ctx)` отдаёт по каждому
subject'у возраст самого старого и число неопубликованных (не-dead) событий.
