# outboxkit

Transactional outbox для сервисов stit. `Bus` вставляет событие в outbox-таблицу
в той же транзакции, что и доменный state; `Worker` тиком — одной транзакцией
list → publish → mark — вычитывает неопубликованные строки и публикует их в
JetStream через `natskit.StreamBus`, помечая published только подтверждённые
`PubAck`'ом. Гарантия доставки — at-least-once; id строки уходит как
`Nats-Msg-Id`, поэтому переотправка на ретрае дедуплицируется стримом.

Это решает классическую проблему dual-write: «закоммитили в БД, но упали до
публикации в брокер» (или наоборот). Outbox делает запись факта и его публикацию
одним атомарным коммитом.

Хранилище — Postgres **14 или новее**: retention отцепляет партиции
`ALTER TABLE … DETACH PARTITION … CONCURRENTLY`.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/outboxkit"
import "git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
```

Версия модуля — см. [`../README.md`](../README.md) (§ Использование в проекте).

## Быстрый старт

Single-instance путь (одна таблица `outbox`, fx-проводка). Нужны `logkit`,
`pgkit` (с `MigrateModule` — он применяет миграции кита), `natskit` (с
`NATS_JETSTREAM_ENABLED=true` и стримом, покрывающим subject'ы outbox'а) и
`timerkit` (он гоняет `Worker.Run` по тикеру; без `timerkit.Module` граф не
собирается — `missing type: timerkit.scheduler`).

```go
fx.New(
    fx.Provide(
        logkit.NewConfig,
        pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig,
        natskit.NewConfig,
        pgstore.NewConfig, outboxkit.NewConfig, // OUTBOX_*
    ),
    logkit.Module,
    pgkit.Module,        // *pgkit.DB, pgkit.Transactor
    pgkit.MigrateModule, // применяет pgstore.Migrations на старте
    natskit.Module(""),  // natskit.StreamBus
    timerkit.Module,     // гоняет Worker.Run и keeper-job'ы

    pgstore.Module,   // Store как outboxkit.Repository, миграции, партиции, retention
    outboxkit.Module, // Bus, Publisher, Worker, drain-Job, backlog-gauge'и

    fx.Invoke(func(p outboxkit.Publisher) { /* положить в доменный сервис */ }),
)
```

Публикация — в той же транзакции, что и доменная запись (атомарность):

```go
err := transactor.WithTx(ctx, func(ctx context.Context) error {
    if err := repo.Save(ctx, order); err != nil {
        return err
    }
    return publisher.Publish(ctx, "order.created", OrderCreated{ID: order.ID})
})
```

`OUTBOX_REQUIRE_TX=true` (default) отвергает `Publish` вне транзакции
(`outboxkit.ErrNoTx`).

## Документация

- [docs/architecture.md](docs/architecture.md) — как устроено: transactional
  outbox, транзакционный тик `Worker`'а и гарантии при нескольких репликах,
  порядок публикации, backoff и poison→dead, lifecycle остановки, партиции и
  retention, single- vs multi-instance, namespaced `Nats-Msg-Id`, решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV,
  тюнинг publish-mode / max-attempts / backoff / tick-timeout / retention,
  миграции и таблицы.
- [docs/observability.md](docs/observability.md) — метрики (label `instance`),
  спаны; `outbox_lag_seconds` / `outbox_pending_count` через `BacklogReporter`.
- [docs/usage.md](docs/usage.md) — рецепты: single-instance `Module`,
  multi-instance `outboxkit.Instance`, оживление dead-строк, ручной drain,
  `BacklogReporter`.
- [docs/testing.md](docs/testing.md) — fake `Repository`/`Transport`, testcontainers,
  тесты конкурентных воркеров, `testkit/outboxtest`.

Источник правды по поведению — код пакета (`outboxkit/*.go`, `outboxkit/pgstore/`).
