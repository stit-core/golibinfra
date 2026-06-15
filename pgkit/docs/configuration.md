# pgkit — конфигурация

Все значения читаются из окружения через `envconfig`. `Module` сам Config'и
**не** provide'ит — caller регистрирует фабрики (`NewPoolConfig`,
`NewReplicaConfig`) или собирает App-структуру (см.
[architecture.md § fx-композиция](architecture.md)).

```go
fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig)
```

## `PoolConfig` (`pool.go`)

Минимум для старта — `PG_DSN`. Остальное имеет рабочие default'ы.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `PG_DSN` | `DSN` | `string` | — (`required`) | строка подключения к primary Postgres |
| `PG_MAX_CONNS` | `MaxConns` | `int32` | `10` | максимум соединений в пуле |
| `PG_MIN_CONNS` | `MinConns` | `int32` | `2` | минимум поддерживаемых тёплых соединений |
| `PG_MAX_CONN_LIFETIME` | `MaxConnLifetime` | `time.Duration` | `30m` | время жизни соединения до плановой замены |
| `PG_MAX_CONN_IDLE_TIME` | `MaxConnIdleTime` | `time.Duration` | `5m` | простой, после которого idle-соединение закрывается |
| `PG_HEALTH_CHECK` | `HealthCheck` | `time.Duration` | `30s` | период фоновой проверки соединений пула (`HealthCheckPeriod`) |
| `PG_OTEL_TRACE` | `OTelTrace` | `bool` | `true` | вешать ли `otelpgx`-tracer на соединения (per-query спаны) |
| `PG_STATEMENT_TIMEOUT` | `StatementTimeout` | `time.Duration` | `30s` | `SET statement_timeout` на каждое соединение |
| `PG_IDLE_IN_TX_SESSION_TIMEOUT` | `IdleInTxSessionTimeout` | `time.Duration` | `60s` | `SET idle_in_transaction_session_timeout` |
| `PG_LOCK_TIMEOUT` | `LockTimeout` | `time.Duration` | `5s` | `SET lock_timeout` |

Сессионные таймауты переводятся в миллисекунды и применяются в `AfterConnect`;
значение `<= 0` нормализуется в `0` (без лимита). Детали — в
[architecture.md § Пул соединений](architecture.md).

## `ReplicaConfig` (`replica.go`)

Опциональная read-replica. Если `PG_REPLICA_DSN` пуст — реплики нет, `Read()`
идёт на мастер.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `PG_REPLICA_DSN` | `ReplicaDSN` | `string` | `""` | строка подключения к read-replica; пусто → реплики нет |
| `PG_REPLICA_MAX_CONNS` | `MaxConns` | `int32` | `10` | максимум соединений в пуле реплики |

Пул реплики наследует от `PoolConfig` остальные параметры (`MinConns`,
`MaxConnLifetime`, `MaxConnIdleTime`, `HealthCheck`, `OTelTrace`); собственными у
реплики являются только DSN и лимит соединений.

## `MigrateRunnerConfig` (`migrate_module.go`)

Используется `MigrateModule` для прогона миграций на старте приложения.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `PG_MIGRATIONS_PATH` | `Path` | `string` | `/migrations` | каталог с файлами `*.up.sql`/`*.down.sql` |

DSN для миграций `MigrateModule` берёт из `PoolConfig.DSN` (тот же `PG_DSN`).

## Public API

Структуры `pgkit.PoolConfig` и `pgkit.ReplicaConfig` — **part of public API**.
Переименование полей, смена envconfig-тегов или типов = breaking change и идёт
мажорным апгрейдом. См. корневой [README § Стабильность API](../../README.md).

## Тюнинг пула

- **`MaxConns` / `MinConns`.** `MaxConns` — потолок параллелизма к БД на инстанс
  сервиса; при N инстансах суммарная нагрузка на Postgres ≈ `N × MaxConns`,
  поэтому сверяй с `max_connections` сервера. `MinConns` держит тёплые
  соединения, чтобы не платить за коннект на пике.
- **`MaxConnLifetime` / `MaxConnIdleTime`.** Плановая ротация соединений; помогает
  при балансировщиках/PgBouncer и утечках состояния сессии. Лайфтайм закрывает
  даже активно используемые соединения по расписанию, idle-time — только
  простаивающие.
- **`StatementTimeout`.** Защита от зависших запросов. Длинные операции (миграции,
  батч-джобы) могут упереться в него — для них поднимай таймаут на уровне сессии
  внутри транзакции или используй отдельный пул.
- **`IdleInTxSessionTimeout`.** Обрывает транзакции, которые открыли и забыли
  (висящая транзакция держит блокировки и тормозит autovacuum).
- **`LockTimeout`.** Ограничивает ожидание блокировки; небольшое значение
  превращает дедлок-подобные ожидания в быструю ошибку, которую можно повторить
  (см. `IsRetryable` в [observability.md](observability.md) и
  [usage.md](usage.md)).
- **`OTelTrace`.** Оставляй `true` в проде — это источник per-query спанов. В
  бенчмарках/нагрузочных прогонах можно выключить, чтобы убрать оверхед трейсинга.
