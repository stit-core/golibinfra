# pgkit — конфигурация

Все значения читаются из окружения через `envconfig`. `Module` и
`MigrateModule` сами Config'и **не** provide'ят — caller регистрирует фабрики
(`NewPoolConfig`, `NewReplicaConfig`, `NewMigrateRunnerConfig`) или собирает
App-структуру (см. [architecture.md § fx-композиция](architecture.md)).

```go
fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig, pgkit.NewMigrateRunnerConfig)
```

## `PoolConfig` (`pool.go`)

Минимум для старта — `PG_DSN`. Остальное имеет рабочие default'ы.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `PG_DSN` | `DSN` | `string` | — (`required`) | строка подключения к primary Postgres (URL или keyword/value) |
| `PG_MAX_CONNS` | `MaxConns` | `int32` | `10` | максимум соединений в пуле |
| `PG_MIN_CONNS` | `MinConns` | `int32` | `2` | минимум поддерживаемых тёплых соединений |
| `PG_MAX_CONN_LIFETIME` | `MaxConnLifetime` | `time.Duration` | `30m` | время жизни соединения до плановой замены |
| `PG_MAX_CONN_IDLE_TIME` | `MaxConnIdleTime` | `time.Duration` | `5m` | простой, после которого idle-соединение закрывается |
| `PG_HEALTH_CHECK` | `HealthCheck` | `time.Duration` | `30s` | период фоновой проверки соединений пула (`HealthCheckPeriod`) |
| `PG_CONNECT_TIMEOUT` | `ConnectTimeout` | `time.Duration` | `10s` | таймаут установки каждого физического соединения и стартового `Ping`; `<= 0` — без лимита |
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

Пул реплики наследует от `PoolConfig` все остальные параметры (`MinConns`,
lifetime/idle, `HealthCheck`, `ConnectTimeout`, `OTelTrace`, сессионные
таймауты); собственными у реплики являются только DSN и лимит соединений.

## `MigrateRunnerConfig` (`migrate_module.go`)

Используется `MigrateModule` для прогона миграций на старте приложения.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `PG_MIGRATIONS_PATH` | `Path` | `string` | `/migrations` | каталог с `*.up.sql`/`*.down.sql` приложения (источник `app`); пустая строка — у приложения нет своих миграций, применяются только источники китов |

DSN для миграций `MigrateModule` берёт из `PoolConfig.DSN` (тот же `PG_DSN`).
Таймаут коннекта мигратора — дедлайн `OnStart` (`fx.StartTimeout`).
Несуществующий или пустой каталог — ошибка старта.

## Public API

Структуры `pgkit.PoolConfig`, `pgkit.ReplicaConfig` и
`pgkit.MigrateRunnerConfig` — **part of public API**. Переименование полей,
смена envconfig-тегов или типов = breaking change и идёт мажорным апгрейдом.
См. корневой [README § Стабильность API](../../README.md).

## Тюнинг пула

- **`MaxConns` / `MinConns`.** `MaxConns` — потолок параллелизма к БД на инстанс
  сервиса; при N инстансах суммарная нагрузка на Postgres ≈ `N × MaxConns`,
  поэтому сверяй с `max_connections` сервера. `MinConns` держит тёплые
  соединения, чтобы не платить за коннект на пике.
- **`MaxConnLifetime` / `MaxConnIdleTime`.** Плановая ротация соединений; помогает
  при балансировщиках/PgBouncer и утечках состояния сессии. Лайфтайм закрывает
  даже активно используемые соединения по расписанию, idle-time — только
  простаивающие.
- **`ConnectTimeout`.** Ограничивает зависание на недоступном хосте (SYN без
  ответа, TLS-handshake): стартовый `Ping` и каждый реконнект пула завершаются
  ошибкой, а не ждут TCP-таймаута ОС.
- **`StatementTimeout`.** Защита от зависших запросов. Длинные операции (миграции,
  батч-джобы) могут упереться в него — для них поднимай таймаут на уровне сессии
  внутри транзакции или используй отдельный пул. Мигратор `MigrateModule`
  открывает собственное соединение и этот лимит не наследует.
- **`IdleInTxSessionTimeout`.** Обрывает транзакции, которые открыли и забыли
  (висящая транзакция держит блокировки и тормозит autovacuum). Код, который
  держит транзакцию, ожидая внешнюю систему (например, тик `outboxkit`), обязан
  укладываться в этот лимит.
- **`LockTimeout`.** Ограничивает ожидание блокировки; небольшое значение
  превращает дедлок-подобные ожидания в быструю ошибку `55P03`
  (`IsLockNotAvailable`), которую можно повторить.
- **`OTelTrace`.** Оставляй `true` в проде — это источник per-query спанов. В
  бенчмарках/нагрузочных прогонах можно выключить, чтобы убрать оверхед трейсинга.
