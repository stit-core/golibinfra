# outboxkit — рецепты

Сигнатуры приведены как в коде; импорты опущены ради краткости.

## Single-instance: fx Module

Одна таблица `outbox`, проводка через fx. `pgstore.Module` даёт `Store` как
`outboxkit.Repository`, регистрирует миграции кита в `pgkit.MigrateModule`,
создаёт партиции в `OnStart` и держит partition/retention job'ы;
`outboxkit.Module` — `Bus`/`Publisher`/`Worker`/drain-`Job`/backlog-gauge'и.
Конфиги caller провайдит явно.

```go
fx.New(
    fx.Provide(
        logkit.NewConfig,
        pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig,
        natskit.NewConfig,
        pgstore.NewConfig, outboxkit.NewConfig,
    ),
    logkit.Module,
    pgkit.Module,        // *pgkit.DB, pgkit.Transactor
    pgkit.MigrateModule, // применяет pgstore.Migrations, провайдит pgkit.Migrated
    natskit.Module(""),  // natskit.StreamBus: NATS_JETSTREAM_ENABLED=true, иначе старт падает
    timerkit.Module,     // гоняет Worker.Run и keeper-job'ы по тикерам

    pgstore.Module,   // Store→Repository, миграции, партиции, retention
    outboxkit.Module, // Bus, Publisher, Worker, drain-Job

    fx.Invoke(func(p outboxkit.Publisher) {
        // передать Publisher в доменный сервис
    }),
)
```

Порядок модулей в `fx.New` не важен: и `OnStart`-хук партиций, и keeper-job'ы
(`RunOnStart` у partition-keeper'а) зависят от `pgkit.Migrated`, поэтому встают в
очередь после прогона миграций. Зависимость `optional` — сборка без
`pgkit.MigrateModule` (схему накатывает CI) остаётся рабочей, но тогда порядок
обеспечивает уже сам CI.

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

`payload` — любое значение, сериализуемое `json.Marshal`; сериализует его `Bus`.
Вне транзакции (при `OUTBOX_REQUIRE_TX=true`, default) `Publish` возвращает
`outboxkit.ErrNoTx`, тело больше `OUTBOX_MAX_PAYLOAD_BYTES` в JSONB-представлении хранилища —
`outboxkit.ErrPayloadTooLarge`,
subject вне `WithSubjects` инстанса — `outboxkit.ErrSubjectNotDeclared`.

## Multi-instance: outboxkit.Instance

Несколько независимых outbox'ов (таблица на домен) в одном приложении.
`pgstore.FactoryModule` провайдит фабрику store'ов, `outboxkit.Instance` собирает
инстанс целиком: store на свою таблицу, схему таблицы в группе `pgkit.migrations`,
транспорт с namespace'ным `Nats-Msg-Id`, drain-job, партиции и retention,
backlog-gauge'и и проверку старта.

```go
// сигнатуры
func pgstore.TableMigrations(table string) pgkit.Source
func outboxkit.Instance(name, table string, opts ...outboxkit.InstanceOption) fx.Option
func outboxkit.WithInstanceConfig(cfg outboxkit.Config) outboxkit.InstanceOption
func outboxkit.WithSubjects(filters ...string) outboxkit.InstanceOption
```

```go
fx.New(
    fx.Provide(logkit.NewConfig, pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig, natskit.NewConfig,
        pgstore.NewConfig, outboxkit.NewConfig),
    logkit.Module, pgkit.Module, pgkit.MigrateModule, natskit.Module(""), timerkit.Module,

    pgstore.FactoryModule, // outboxkit.RepositoryFactory
    outboxkit.Instance("orders", "outbox_orders", outboxkit.WithSubjects("orders.>")),
    outboxkit.Instance("leads", "outbox_leads", outboxkit.WithSubjects("leads.>"),
        outboxkit.WithInstanceConfig(orderedCfg)), // свой режим у одного домена

    fx.Invoke(fx.Annotate(
        func(orders outboxkit.Publisher) { /* в доменный сервис */ },
        fx.ParamTags(`name:"orders"`),
    )),
)
```

Схема таблицы инстанса — `pgstore.TableMigrations(table)` с таблицей версий
`schema_migrations_<table>`: `pgkit.MigrateModule` берёт её из группы и
накатывает на старте. Отдельно её не регистрируют — второй источник с тем же
именем `MigrateModule` отвергает (`duplicate source name`). Без `MigrateModule`
(миграции накатывает отдельный шаг) тот же источник передают в
`pgkit.MigrateUpAll`. На старте инстанс сверяет схему таблицы с этим источником
(`outboxkit.SchemaChecker` хранилища, после миграций): не применён — ошибка старта с
именем инстанса и `pgkit.ErrSchema`, а не первая публикация.

`WithSubjects` объявляет subject'ы событий инстанса — фильтры NATS. Старт проверяет, что
JetStream включён и каждый фильтр целиком накрыт одним стримом (`NATS_JS_STREAM_NAMES=orders`
даёт стрим `orders` с subject'ами `orders.>`), иначе — ошибка старта с именем инстанса;
`Publish` вне объявленных — `outboxkit.ErrSubjectNotDeclared`. Без опции старт проверяет
только JetStream.

`WithMetricSubject(fn)` сворачивает subject события в шаблон — label `subject` метрик и
backlog-gauge'ей инстанса и шаблон в сигналах `natskit`; без опции label нет. Subject — тип
события, id — в payload; если id всё же в subject, `fn` возвращает шаблон:

```go
outboxkit.Instance("orders", "outbox_orders", outboxkit.WithMetricSubject(func(subject string) string {
    parts := strings.Split(subject, ".") // orders.42.created
    if len(parts) == 3 {
        parts[1] = "*"
    }
    return strings.Join(parts, ".") // orders.*.created
}))
```

Таблица `outbox` (`pgstore.Module` и `outboxkit.Module`) и инстансы
(`pgstore.FactoryModule` и `outboxkit.Instance`) сочетаются в одном графе.

`*Outbox` и `Publisher` инстанса провайдятся под fx-тегом `name:"<name>"`; имя
инстанса становится label'ом `instance` в метриках и namespace'ом `Nats-Msg-Id`
(`"orders:42"`), что исключает cross-table коллизии дедупа, когда таблицы делят
один JetStream-стрим.

## Multi-instance вручную

Когда сборка не через fx:

```go
// сигнатуры
func pgstore.NewStoreFor(db *pgkit.DB, table string, opts ...pgstore.StoreOption) (*pgstore.Store, error)
func outboxkit.NewStreamTransportFor(sb natskit.StreamBus, namespace string) outboxkit.Transport
func outboxkit.New(name string, cfg outboxkit.Config, repo outboxkit.Repository, tr outboxkit.Transport, tx pgkit.Transactor, log logkit.Logger) (*outboxkit.Outbox, error)
```

```go
store, err := pgstore.NewStoreFor(db, "outbox_orders",
    pgstore.WithRequireTx(), pgstore.WithRetention(3))
if err != nil {
    return err
}
orders, err := outboxkit.New("orders", cfg, store, outboxkit.NewStreamTransportFor(sb, "orders"), transactor, log)
if err != nil {
    return err // невалидный cfg — ErrConfig
}

publisher := orders.Publisher() // туда, где нужна доменная публикация
jobs := orders.Jobs()           // drain + партиции + retention → timerkit.JobsGroup
reg, err := orders.RegisterBacklogGauges() // Unregister на shutdown
```

Схему кастомной таблицы накатывает caller — `pgstore.TableMigrations(table)` в
`pgkit.MigrateUpAll`/`MigrateModule` либо `pgstore.SchemaSQL(table)` в
собственной миграции приложения; `store.CheckSchema(ctx)` сверяет таблицу с источником
`TableMigrations(table)` (у `NewStore` — `Migrations`). В `log` воркер пишет переход строки в dead (`nil` —
`logkit.Nop()`). Проверку стримов на старте ручная сборка делает сама —
`natskit.StreamChecker` шины (рецепт — `natskit/docs/usage.md`).

## Оживление dead-строк

Строки, исчерпавшие `OUTBOX_MAX_ATTEMPTS`, остаются в таблице с `dead_at`,
`attempts` и `last_error`; каждый переход — ERROR `outboxkit: event dead` в логе. Вернуть их
в очередь (после починки причины):

```go
func (o *outboxkit.Outbox) ReviveDead(ctx context.Context, f outboxkit.ReviveFilter) (int64, error)

n, err := outbox.ReviveDead(ctx, outboxkit.ReviveFilter{
    Subject: "order.created",              // пусто — любой subject
    Since:   time.Now().AddDate(0, 0, -7), // нулевое время — без границы
})
```

Фильтр отбирает dead-строки по `IDs`, `Subject` и `Since` (нижняя граница
`created_at`); поля соединяются по И, пустой фильтр берёт все dead-строки
таблицы. Сбрасываются `dead_at`, `attempts`, `last_error` и пауза backoff;
результат — число оживлённых строк, оно же уходит в `outbox_revived_total`.
Хранилище без поддержки замены отвечает `outboxkit.ErrNotSupported`.

## MarkPublished и drain вручную

Обычно `Worker.Run` (через `timerkit.Job`) делает весь цикл list → publish → mark
в одной транзакции. Прямые методы `Repository` доступны, если нужен кастомный
drain:

```go
func (s *pgstore.Store) ListUnpublished(ctx context.Context, limit int, mode outboxkit.PublishMode) ([]outboxkit.Message, error)
func (s *pgstore.Store) MarkPublished(ctx context.Context, refs []outboxkit.MessageRef) error
func (s *pgstore.Store) RecordFailures(ctx context.Context, failures []outboxkit.Failure) error
```

`ListUnpublished` берёт строки с `published_at IS NULL AND dead_at IS NULL`,
пропуская те, чей `next_attempt_at` ещё не наступил (в `ordered` — останавливаясь
на такой строке), сортирует по `created_at, id` и запирает их `FOR UPDATE` до
конца транзакции вызывающего — поэтому вызывается внутри `pgkit.Transactor.WithTx`.
В `batch` занятые другими строки пропускаются (`SKIP LOCKED`), в `ordered`
занятая голова очереди — `outboxkit.ErrBusy` (`NOWAIT`).

`MarkPublished` и `RecordFailures` принимают `outboxkit.MessageRef{ID, CreatedAt}`
— его отдаёт `Message.Ref()`. `CreatedAt` — ключ партиционирования: с ним UPDATE
идёт по партициям батча, а не по всем партициям таблицы.

## BacklogReporter (lag / pending)

`pgstore.Store` реализует `outboxkit.BacklogReporter`. `outboxkit.Module` и
`outboxkit.Instance` подключают gauge'и автоматически. Свой `BacklogReporter` обязан уважать
`ctx` метода `Backlog`: срок снимка (5 с) действует через него, и реализация, которая `ctx` не
читает, на зависшем хранилище оставляет gauge'и на последнем значении. Вручную:

```go
func (o *outboxkit.Outbox) RegisterBacklogGauges() (metric.Registration, error)
func outboxkit.RegisterBacklogGauges(instance string, reporter outboxkit.BacklogReporter) (metric.Registration, error)

reg, err := outbox.RegisterBacklogGauges()
if err != nil {
    return err
}
// на shutdown:
defer reg.Unregister()
```

Метод `(*Outbox).RegisterBacklogGauges` всегда отдаёт непустую `Registration`:
на хранилище без `BacklogReporter` это no-op, и `defer reg.Unregister()` не
требует проверки на `nil`. Пакетная `outboxkit.RegisterBacklogGauges(instance,
reporter)` берёт `BacklogReporter` явным аргументом.

Это включает `outbox_lag_seconds{instance,subject}` и
`outbox_pending_count{instance,subject}` (см.
[observability.md](observability.md)). `Store.Backlog(ctx)` отдаёт по каждому
subject'у возраст самого старого и число неопубликованных (не-dead) событий. Запрос идёт
вне сбора метрик: первый снимок — при регистрации, следующий заказывает каждый сбор, gauge'и
отдают последний готовый; `Unregister` прерывает идущий запрос.
