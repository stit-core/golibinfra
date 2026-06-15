# outboxkit — рецепты

Сигнатуры приведены как в коде; импорты опущены ради краткости.

## Single-instance: fx Module

Одна таблица `outbox`, проводка через fx. `pgstore.Module` даёт `Store` как
`outboxkit.Repository` плюс partition/retention jobs; `outboxkit.Module` —
`Bus`/`Publisher`/`Worker`/drain-`Job`. Конфиг `outboxkit.NewConfig` caller
провайдит явно (Module его не provide'ит).

```go
fx.New(
    pgkit.Module,    // *pgkit.DB, Transactor
    natskit.Module,  // natskit.StreamBus (NATS_JETSTREAM_ENABLED=true + стрим)
    timerkit.Module, // гоняет Worker.Run и keeper-jobs по тикерам

    pgstore.Module,                  // Store→Repository, partition + retention keepers
    fx.Provide(outboxkit.NewConfig), // OUTBOX_* конфиг
    outboxkit.Module,                // Bus, Publisher, Worker, drain-Job

    fx.Invoke(func(p outboxkit.Publisher) {
        // передать Publisher в доменный сервис
    }),
)
```

Прогнать embedded-миграцию `pgstore.MigrationsFS` (через migrate-runner `pgkit`)
до старта приложения. tx-strict guard включается `OUTBOX_REQUIRE_TX=true`.

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

Типизированный helper:

```go
func PublishJSON[T any](b *outboxkit.Bus, ctx context.Context, subject string, msg T) error
```

## Multi-instance: New + NewStoreFor + NewStreamTransportFor

Несколько независимых outbox'ов (таблица на домен) в одном приложении.
Собирается вручную, потому что каждый инстанс — своя таблица и свой namespace для
`Nats-Msg-Id`.

```go
// сигнатуры
func pgstore.NewStoreFor(db *pgkit.DB, table string, opts ...pgstore.StoreOption) (*pgstore.Store, error)
func outboxkit.NewStreamTransportFor(sb natskit.StreamBus, namespace string) outboxkit.Transport
func outboxkit.New(name string, repo outboxkit.Repository, tr outboxkit.Transport, cfg outboxkit.Config) *outboxkit.Outbox
```

```go
// на каждый домен — своя таблица + namespace'ный транспорт
ordersStore, err := pgstore.NewStoreFor(db, "outbox_orders", pgstore.WithRequireTx())
if err != nil {
    return err
}
ordersTr := outboxkit.NewStreamTransportFor(sb, "orders") // Msg-Id = "orders:<id>"
orders := outboxkit.New("orders", ordersStore, ordersTr, cfg)

leadsStore, _ := pgstore.NewStoreFor(db, "outbox_leads")
leadsTr := outboxkit.NewStreamTransportFor(sb, "leads")    // Msg-Id = "leads:<id>"
leads := outboxkit.New("leads", leadsStore, leadsTr, cfg)
```

namespace обязателен, если таблицы делят один JetStream-стрим: per-domain
`BIGSERIAL` независимы, и голый id столкнулся бы (см.
[architecture.md](architecture.md) § Namespaced Nats-Msg-Id).

Проводка инстансов: `Outbox.Publisher()` — туда, где нужна доменная публикация;
`Outbox.Job()` (`timerkit.Job` с интервалом из конфига) — в `timer.job`-группу.
Схему и партиции кастомных таблиц заводит caller — `pgstore.SchemaSQL(table)` +
`Store.EnsurePartitions(ctx, monthsAhead)`; retention — `Store.DropOldPartitions(ctx, keepMonths)`
(оба можно завернуть в `timerkit.Job`, как `pgstore.PartitionKeeperJob` /
`RetentionKeeperJob`).

## MarkPublished и drain вручную

Обычно `Worker.Run` (через `timerkit.Job`) делает весь цикл list → publish → mark.
Прямые методы `Repository` доступны, если нужен кастомный drain:

```go
func (s *pgstore.Store) ListUnpublished(ctx context.Context, limit int) ([]outboxkit.Message, error)
func (s *pgstore.Store) MarkPublished(ctx context.Context, ids []int64) error
func (s *pgstore.Store) RecordFailures(ctx context.Context, failures []outboxkit.Failure) error
```

`ListUnpublished` берёт только строки с `published_at IS NULL AND dead_at IS NULL`,
сортирует по `created_at, id`, использует `FOR UPDATE SKIP LOCKED` (несколько
воркеров не дерутся за одни строки). `MarkPublished` — один batch-`UPDATE` по
`id = ANY($2)`, метит только подтверждённые. `RecordFailures` одним `UPDATE` через
`unnest` инкрементит `attempts`, пишет `last_error` и проставляет `dead_at` тем, у
кого `Failure.Dead = true`.

## BacklogReporter (lag / pending)

`pgstore.Store` реализует `outboxkit.BacklogReporter`. Single-instance `Module`
подключает gauge'и автоматически. Вручную:

```go
func outboxkit.RegisterBacklogGauges(reporter outboxkit.BacklogReporter) (metric.Registration, error)

reg, err := outboxkit.RegisterBacklogGauges(store)
if err != nil {
    return err
}
// на shutdown:
defer reg.Unregister()
```

Это включает `outbox_lag_seconds{subject}` и `outbox_pending_count{subject}` (см.
[observability.md](observability.md)). `Store.Backlog(ctx)` отдаёт по каждому
subject'у возраст самого старого и число неопубликованных (не-dead) событий.
