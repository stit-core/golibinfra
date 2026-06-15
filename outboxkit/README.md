# outboxkit

Transactional outbox для сервисов stit. `Bus` вставляет событие в outbox-таблицу
в той же транзакции, что и доменный state; `Worker` батчами вычитывает
неопубликованные строки и публикует их в JetStream через `natskit.StreamBus`,
помечая published только подтверждённые `PubAck`'ом. Гарантия доставки —
at-least-once; id строки уходит как `Nats-Msg-Id`, поэтому переотправка на ретрае
дедуплицируется стримом.

Это решает классическую проблему dual-write: «закоммитили в БД, но упали до
публикации в брокер» (или наоборот). Outbox делает запись факта и его публикацию
одним атомарным коммитом.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/outboxkit"
import "git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
```

Версия модуля — см. [`../README.md`](../README.md) (§ Использование в проекте).

## Быстрый старт

Single-instance путь (одна таблица `outbox`, fx-проводка). Нужны
`pgkit`, `natskit` (с `NATS_JETSTREAM_ENABLED=true` и стримом, покрывающим
subject'ы outbox'а) и `timerkit` (он гоняет `Worker.Run` по тикеру).

```go
fx.New(
    pgkit.Module,
    natskit.Module,
    timerkit.Module,

    pgstore.Module,                  // Store как outboxkit.Repository + partition/retention jobs
    fx.Provide(outboxkit.NewConfig), // OUTBOX_* конфиг (caller провайдит явно)
    outboxkit.Module,                // Bus, Publisher, Worker, drain-Job

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

## Документация

- [docs/architecture.md](docs/architecture.md) — как устроено: transactional
  outbox, `Worker`→JetStream, poison→dead, retention партиций, single- vs
  multi-instance, namespaced `Nats-Msg-Id`, ключевые решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV,
  тюнинг publish-mode / max-attempts / retention, миграции и таблицы.
- [docs/observability.md](docs/observability.md) — метрики, labels, спаны;
  `outbox_lag_seconds` / `outbox_pending_count` opt-in через `BacklogReporter`.
- [docs/usage.md](docs/usage.md) — рецепты: single-instance `Module`,
  multi-instance `New` + `NewStoreFor` + `NewStreamTransportFor`, `MarkPublished`,
  `BacklogReporter`.
- [docs/testing.md](docs/testing.md) — fake `Repository`/`Transport`, testcontainers,
  поведение в `-short`.

Источник правды по поведению — код пакета (`outboxkit/*.go`, `outboxkit/pgstore/`).
