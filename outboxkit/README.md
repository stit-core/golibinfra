# outboxkit

Generic transactional-outbox для golibinfra. Гарантирует at-least-once
доставку событий, согласованную с бизнес-записью.

## Принцип

`Bus.Publish` НЕ отправляет сообщение в брокер напрямую — он делает `INSERT`
в таблицу `outbox`. Этот `INSERT` обязан выполняться **внутри той же
транзакции**, что и бизнес-запись (через `pgkit.Transactor.WithTx` —
`pgkit.DB` tx-aware: `Exec/Query/QueryRow` берут tx из контекста).

```go
err := tr.WithTx(ctx, func(ctx context.Context) error {
    if err := userRepo.Save(ctx, u); err != nil {
        return err
    }
    return outboxBus.Publish(ctx, "user.created", UserCreated{ID: u.ID})
})
```

Если бизнес-операция упадёт — транзакция откатится, и запись в `outbox`
исчезнет вместе с бизнес-записью. Никаких "ушедших фантомных событий" о
несуществующей сущности.

## Worker

Фоновый `Worker` (через `timerkit.Job`, интервал = `OUTBOX_INTERVAL`,
по умолчанию `1s`) вычитывает пачку (`OUTBOX_BATCH_SIZE`, default `100`)
неопубликованных сообщений, публикует их через `natskit.Bus`, и проставляет
`published_at`. На фейле публикации/маркировки worker возвращает ошибку — на
следующем тике повторит. **At-least-once**: получатели должны быть
идемпотентны.

## fx

```go
fx.New(
    pgkit.PoolModule,
    natskit.Module,
    timerkit.Module,
    outboxkit.Module,
)
```

`outboxkit.Module` регистрирует `Config`, `Repository`, `Bus`, `Worker` и
добавляет `timerkit.Job` в группу `timer.job`.

## Миграции

`outboxkit.Migrations` (`embed.FS`) содержит `0001_outbox.up.sql`. Накатывайте
вместе с бизнес-миграциями (например, скопировав файлы в общий каталог в
`cmd/migrate`).
