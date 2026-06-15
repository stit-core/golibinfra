# pgkit

Фасад поверх [pgx/v5](https://github.com/jackc/pgx): пул соединений (`pgxpool` с
сессионными таймаутами), `*pgkit.DB` для запросов, `Transactor` с проброской
транзакции через `context`, migrate-runner (advisory-lock + multi-source поверх
[golang-migrate](https://github.com/golang-migrate/migrate)), маппинг pg-ошибок в
булевы предикаты, `DBProbe` health-probe и опциональная read-replica
(`ReadWriteDB`). Из коробки: метрики `db_pool_*` и per-query спаны через
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
    telemetry.Module, // даёт metric.Meter → включает db_pool_*
    pgkit.Module,     // *pgxpool.Pool, *pgkit.DB, pgkit.Transactor, *pgkit.ReadWriteDB
    fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig),

    fx.Invoke(func(db *pgkit.DB, tx pgkit.Transactor) {
        _ = tx.WithTx(context.Background(), func(ctx context.Context) error {
            _, err := db.Exec(ctx, `INSERT INTO users(email) VALUES ($1)`, "a@b.c")
            return err // ошибка → Rollback, nil → Commit
        })
    }),
)
```

Минимальный ENV: `PG_DSN`. `Module` сам Config'и не provide'ит — их регистрирует
caller (`NewPoolConfig`/`NewReplicaConfig` или App-структура). Запрос внутри
`WithTx` атомарно идёт в транзакцию из `ctx`; вне неё — в пул.

> Прямые конструкторы (`NewPool`, `NewDB`, `NewPoolTransactor`,
> `NewReadWriteDB`) доступны для тестов и не-fx-кода, но менее идиоматичны.

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство пула, `DB`-фасада,
  `Transactor`, migrate-runner'а (advisory-lock + multi-source), read-replica и
  маппинга ошибок; проектные решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV
  (`PoolConfig` + `ReplicaConfig` + `PG_MIGRATIONS_PATH`) и тюнинг пула.
- [docs/observability.md](docs/observability.md) — метрики `db_pool_*` (9 шт.),
  labels, per-query спаны `otelpgx` (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: fx Module, транзакции, обработка
  pg-ошибок, миграции, read-replica, health-probe.
- [docs/testing.md](docs/testing.md) — интеграционные тесты через
  `testkit.StartPostgres`, in-memory миграции, проверка метрик пула.
