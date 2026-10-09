# pgkit — конфигурация

Все значения читаются из окружения ридером ENV китов ([корневой README § Стабильность API](../../README.md#стабильность-api)). `Module` и
`MigrateModule` сами Config'и **не** provide'ят — caller регистрирует фабрики
(`NewPoolConfig`, `NewReplicaConfig`, `NewMigrateRunnerConfig`) или собирает
App-структуру (см. [architecture.md § fx-композиция](architecture.md)).

```go
fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig, pgkit.NewMigrateRunnerConfig)
```

Каждый `New*Config` валидирует прочитанное и падает на старте: нарушение
инварианта — ошибка, обёрнутая `pgkit.ErrConfig` (`errors.Is(err,
pgkit.ErrConfig)`). Те же проверки делают `PoolConfig.Validate()`,
`ReplicaConfig.Validate()`, `MigrateRunnerConfig.Validate()` и `NewPool` — путь
через `fx.Supply` со структурой конфига тоже проверяется.

## `PoolConfig` (`pool.go`)

Минимум для старта — `PG_DSN`. Остальное имеет рабочие default'ы.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `PG_DSN` | `DSN` | `string` | — (`required`) | строка подключения к primary Postgres (URL или keyword/value) |
| `PG_MAX_CONNS` | `MaxConns` | `int32` | `10` | максимум соединений в пуле; `0` — дефолт pgxpool |
| `PG_MIN_CONNS` | `MinConns` | `int32` | `2` | минимум поддерживаемых тёплых соединений; `0` — пул не греется |
| `PG_MAX_CONN_LIFETIME` | `MaxConnLifetime` | `time.Duration` | `30m` | время жизни соединения до плановой замены; `0` — дефолт pgxpool |
| `PG_MAX_CONN_IDLE_TIME` | `MaxConnIdleTime` | `time.Duration` | `5m` | простой, после которого idle-соединение закрывается; `0` — дефолт pgxpool |
| `PG_HEALTH_CHECK` | `HealthCheck` | `time.Duration` | `30s` | период фоновой проверки соединений пула (`HealthCheckPeriod`); `0` — дефолт pgxpool |
| `PG_CONNECT_TIMEOUT` | `ConnectTimeout` | `time.Duration` | `10s` | таймаут установки каждого физического соединения и стартового `Ping`; `0` — не переопределять (действует `connect_timeout` из DSN, а без него — лимита нет) |
| `PG_OTEL_TRACE` | `OTelTrace` | `bool` | `true` | вешать ли `otelpgx`-tracer на соединения (спаны запросов, connect и acquire) |
| `PG_STATEMENT_TIMEOUT` | `StatementTimeout` | `time.Duration` | `30s` | `statement_timeout` сессии; `0` — без лимита |
| `PG_IDLE_IN_TX_SESSION_TIMEOUT` | `IdleInTxSessionTimeout` | `time.Duration` | `60s` | `idle_in_transaction_session_timeout` сессии; `0` — без лимита |
| `PG_LOCK_TIMEOUT` | `LockTimeout` | `time.Duration` | `5s` | `lock_timeout` сессии; `0` — без лимита |

`Validate` (и значит `NewPoolConfig`/`NewPool`) требует: непустой `PG_DSN`,
неотрицательные размеры и duration'ы, `PG_MIN_CONNS <= PG_MAX_CONNS` при
`PG_MAX_CONNS > 0`.

Ноль в размерах и таймаутах пула означает **дефолт pgxpool**, а не «выключено»:
`PoolConfig{DSN: dsn}` даёт рабочий пул. Сессионные таймауты переводятся в
миллисекунды и уезжают в StartupMessage через `ConnConfig.RuntimeParams`: ноль —
без лимита, и он перекрывает `ALTER ROLE` и `ALTER DATABASE … SET`. Детали — в
[architecture.md § Пул соединений](architecture.md).

## `ReplicaConfig` (`replica.go`)

Опциональная read-replica. Если `PG_REPLICA_DSN` пуст — реплики нет, `Read()`
идёт на мастер.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `PG_REPLICA_DSN` | `ReplicaDSN` | `string` | `""` | строка подключения к read-replica; пусто → реплики нет |
| `PG_REPLICA_MAX_CONNS` | `MaxConns` | `int32` | `10` | максимум соединений в пуле реплики; `0` — дефолт pgxpool |

`ReplicaConfig` **не обязателен** для `Module`: без него (и при пустом
`PG_REPLICA_DSN`) реплики нет и `Read()` идёт на мастер. Zero value валиден;
`Validate` требует лишь неотрицательный `PG_REPLICA_MAX_CONNS`.

Пул реплики наследует от `PoolConfig` все остальные параметры (`MinConns`,
lifetime/idle, `HealthCheck`, `ConnectTimeout`, `OTelTrace`, сессионные
таймауты); собственными у реплики являются только DSN и лимит соединений.
Из наследования следует кросс-инвариант: при `PG_REPLICA_MAX_CONNS > 0` он
должен быть `>= PG_MIN_CONNS` — иначе унаследованный прогрев не помещается в
пул реплики. Нарушение валит старт с `ErrConfig`:
`PG_REPLICA_MAX_CONNS (1) must be >= PG_MIN_CONNS (2)`.

## `MigrateRunnerConfig` (`migrate_module.go`)

Параметры прогона миграций: `MigrateModule` на старте приложения и команда миграций
потребителя (`MigrateUpAll(ctx, dsn, cfg.LockTimeout, …)` под `cfg.Timeout`).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `PG_MIGRATIONS_PATH` | `Path` | `string` | `/migrations` | каталог с `*.up.sql`/`*.down.sql` приложения (источник `app`); `-` (пустая строка в `Path`) — у приложения нет своих миграций, применяются только источники китов |
| `PG_MIGRATE_TIMEOUT` | `Timeout` | `time.Duration` | `2m` | лимит на ожидание advisory-lock'а **и** прогон всех миграций в `OnStart`; на дедлайне идущий оператор прерывается на сервере |
| `PG_MIGRATE_LOCK_TIMEOUT` | `LockTimeout` | `time.Duration` | `5s` | `lock_timeout` соединения мигратора: столько оператор миграции ждёт блокировку таблицы — и столько же за ним стоят запросы к ней; не дождался — миграция падает |

DSN для миграций `MigrateModule` берёт из `PoolConfig.DSN` (тот же `PG_DSN`).
Он же становится таймаутом коннекта мигратора. `Validate` требует
`PG_MIGRATE_TIMEOUT > 0`, `PG_MIGRATE_LOCK_TIMEOUT > 0` и `PG_MIGRATIONS_PATH` без
окружающих пробелов; несуществующий или пустой каталог — ошибка старта.

**Отказ миграции.** Дедлайн `PG_MIGRATE_TIMEOUT` и `PG_MIGRATE_LOCK_TIMEOUT` обрывают
оператор, и миграция падает с версией `dirty=true`, как любой упавший файл: следующий
старт требует ручной починки. Файл без явных `BEGIN`/`COMMIT` выполняется одной
транзакцией и откатывается целиком — после проверки схемы версию возвращают на
предыдущую (`force` CLI golang-migrate). Таймаут блокировки короче — меньше простой
запросов к таблице на деплое, но чаще отказ рядом с долгой транзакцией.

**`PG_MIGRATE_TIMEOUT` и `fx.StartTimeout`.** Хук `OnStart` работает под
меньшим из двух дедлайнов, поэтому `fx.StartTimeout` (по умолчанию 15 с) должен
покрывать `PG_MIGRATE_TIMEOUT` плюс остальной старт — иначе миграции обрывает
fx, а не kit.

## Public API

Структуры `pgkit.PoolConfig`, `pgkit.ReplicaConfig` и
`pgkit.MigrateRunnerConfig` — **part of public API**. Переименование полей,
смена envconfig-тегов или типов = breaking change: выпускается minor-релизом,
помечается `!` в Conventional Commit и строкой миграции в аннотации тега
(см. корневой [README § Версионирование](../../README.md)).

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
- **PgBouncer.** Сессионные таймауты уезжают в StartupMessage соединения. В
  `transaction`-режиме PgBouncer мультиплексирует серверные соединения, и эти
  параметры на них не гарантируются — задавай их на стороне пулера или на
  сервере.
- **`OTelTrace`.** Оставляй `true` в проде — это источник per-query спанов. В
  бенчмарках/нагрузочных прогонах можно выключить, чтобы убрать оверхед трейсинга.
