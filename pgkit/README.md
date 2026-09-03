# pgkit

Фасад поверх [pgx/v5](https://github.com/jackc/pgx): пул соединений (`pgxpool` с
сессионными таймаутами), `*pgkit.DB` для запросов, `Transactor` с проброской
транзакции через `context`, migrate-runner (advisory-lock + независимые
источники миграций поверх
[golang-migrate](https://github.com/golang-migrate/migrate)), маппинг pg-ошибок в
булевы предикаты, `DBProbe` health-probe (`healthkit.Probe`) и опциональная
read-replica (`ReadWriteDB`). Из коробки: метрики `db_pool_*` (label `role`)
через глобальный OTel Meter и per-query спаны через
[otelpgx](https://github.com/exaring/otelpgx).

Kit даёт сервису соединение, надёжность пула и атомарность записи; SQL, схема и
доменные репозитории — на стороне потребителя.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/pgkit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

```go
fx.New(
    pgkit.Module,        // *pgxpool.Pool, *pgkit.DB, pgkit.Transactor, *pgkit.ReadWriteDB
    pgkit.MigrateModule, // миграции китов (группа pgkit.migrations) + PG_MIGRATIONS_PATH в OnStart
    fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig, pgkit.NewMigrateRunnerConfig),
    healthkit.Provide(pgkit.DBProbe), // SELECT 1 в /ready

    fx.Invoke(func(db *pgkit.DB, tx pgkit.Transactor) {
        _ = tx.WithTx(context.Background(), func(ctx context.Context) error {
            _, err := db.Exec(ctx, `INSERT INTO users(email) VALUES ($1)`, "a@b.c")
            return err // ошибка или panic → Rollback, nil → Commit
        })
    }),
)
```

Минимальный ENV: `PG_DSN`. `Module` сам Config'и не provide'ит — их регистрирует
caller (`NewPoolConfig`/`NewReplicaConfig`/`NewMigrateRunnerConfig` или
App-структура). Запрос внутри `WithTx` атомарно идёт в транзакцию из `ctx`; вне
неё — в пул.

> Прямые конструкторы (`NewPool`, `NewDB`, `NewPoolTransactor`,
> `NewReadWriteDB`) доступны для тестов и не-fx-кода, но менее идиоматичны.

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство пула, `DB`-фасада,
  `Transactor`, migrate-runner'а (advisory-lock + таблица версий на источник),
  read-replica и маппинга ошибок; проектные решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV
  (`PoolConfig` + `ReplicaConfig` + `MigrateRunnerConfig`) и тюнинг пула.
- [docs/observability.md](docs/observability.md) — метрики `db_pool_*` (9 шт.),
  label `role`, per-query спаны `otelpgx` (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: fx Module, транзакции, обработка
  pg-ошибок, миграции (киты + приложение), read-replica, health-probe.
- [docs/testing.md](docs/testing.md) — интеграционные тесты через
  `testkit.StartPostgres`, in-memory миграции, проверка метрик пула.
