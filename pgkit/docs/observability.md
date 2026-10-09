# pgkit — observability

Единый источник правды по телеметрии kit'а. Пул инструментируется метриками
`pg_pool_*`, а соединения — спанами через `otelpgx`. Метрики
регистрирует `InstrumentPool(pool, role)` через глобальный OTel Meter
(`otel.Meter("git.stit.tech/stit-core/golibinfra/pgkit")`); в fx-графе это
делает `Module` для primary и replica: регистрация — в `OnStart`, отписка
(`Registration.Unregister`) — в `OnStop`. Серии `pg_pool_*` есть от старта до остановки
приложения; пул графа, который собран и не стартовал, callback в `MeterProvider` не оставляет.

Kit **не выставляет** resource-атрибуты
(`service.name`/`service.version`/`deployment.environment.name`) — их несёт
OTel-resource, настроенный потребителем в его проводке OTel SDK. Без
зарегистрированного `MeterProvider` инструменты — no-op.

## Метрики `pg_pool_*`

Девять метрик, все снимаются в одном `RegisterCallback` поверх `pool.Stat()`.
Gauge'и отражают мгновенное состояние пула, counter'ы — кумулятивные счётчики
pgxpool.

| Имя | Тип | Источник (`pgxpool.Stat`) | Назначение |
|---|---|---|---|
| `pg_pool_acquired_conns` | Int64ObservableGauge | `AcquiredConns()` | соединения, занятые прямо сейчас |
| `pg_pool_idle_conns` | Int64ObservableGauge | `IdleConns()` | простаивающие соединения |
| `pg_pool_total_conns` | Int64ObservableGauge | `TotalConns()` | всего открытых соединений |
| `pg_pool_max_conns` | Int64ObservableGauge | `MaxConns()` | сконфигурированный лимит пула |
| `pg_pool_constructing_conns` | Int64ObservableGauge | `ConstructingConns()` | соединения в процессе установки |
| `pg_pool_empty_acquire_total` | Int64ObservableCounter | `EmptyAcquireCount()` | acquire'ы, ждавшие из-за пустого пула |
| `pg_pool_canceled_acquire_total` | Int64ObservableCounter | `CanceledAcquireCount()` | acquire'ы, отменённые по `ctx` |
| `pg_pool_acquire_total` | Int64ObservableCounter | `AcquireCount()` | всего успешных acquire'ов |
| `pg_pool_acquire_duration_seconds_total` | Float64ObservableCounter (unit `s`) | `AcquireDuration().Seconds()` | суммарное время ожидания acquire |

Гистограмм длительности kit не эмитит: латентность запроса несут спаны
`otelpgx` и его собственная метрика `db.client.operation.duration`.

### Labels

| Label | Значения | Откуда |
|---|---|---|
| `role` | `primary` \| `replica` | `Module`: primary-пул и пул `PG_REPLICA_DSN`; для `InstrumentPool` вручную — аргумент `role` (`pgkit.RolePrimary`/`pgkit.RoleReplica` или своё значение) |

Несколько пулов с одним `role` в одном процессе складываются в одну серию —
давай им разные `role`.

### Чтение

- **Сатурация пула.** `pg_pool_acquired_conns / pg_pool_max_conns` близко к `1`
  плюс растущий `pg_pool_empty_acquire_total` — пул упирается в лимит; повышай
  `PG_MAX_CONNS` (сверяясь с `max_connections` сервера) или снижай длительность
  запросов.
- **Ожидание acquire.** Рост `pg_pool_acquire_duration_seconds_total` относительно
  `pg_pool_acquire_total` (средняя задержка получения соединения) — тот же
  симптом нехватки соединений.
- **Отмены.** `pg_pool_canceled_acquire_total` растёт — запросы отменяются по
  таймауту/`ctx` ещё на стадии ожидания соединения (обычно следствие сатурации).

## Спаны и метрики `otelpgx`

При `PG_OTEL_TRACE=true` (default) на `ConnConfig.Tracer` ставится `otelpgx.NewTracer` с именем
запроса вместо текста SQL в имени спана; pgxpool дополнительно распознаёт его как
`AcquireTracer`. Все спаны — `SpanKind=client`, scope `github.com/exaring/otelpgx`, атрибуты
semconv БД (`db.system.name=postgresql`, `db.namespace`, `server.address`, `server.port`,
`user.name`); у спанов запроса — ещё `db.operation.name` (имя запроса, то же, что в имени спана) и
`db.query.text`. Спаны otelpgx пишутся только внутри записываемого спана вызова (запрос HTTP,
job, обработка сообщения) и встраиваются в его трассу: запрос вне трассы спана не даёт.

| Спан | Когда |
|---|---|
| `query <имя>` (`query GetUser`, `query SELECT`) | `Exec`/`Query`/`QueryRow` и каждый запрос внутри батча |
| `batch start` | начало `SendBatch` |
| `prepare <имя>` | подготовка statement'а (кэш prepared statements pgx) |
| `copy_from <table>` | `CopyFrom` |
| `connect` | установка физического соединения |
| `pool.acquire` | получение соединения из пула |

Имя запроса — имя запроса sqlc из ведущего комментария (`-- name: GetUser :one` → `GetUser`),
иначе первое слово SQL после ведущих комментариев в верхнем регистре (`SELECT`); запрос без
слова — `UNKNOWN`. Текста SQL в имени нет: у него конечное множество значений, литералы запроса
в него не попадают — они в `db.query.text`.

Тот же tracer эмитит две метрики в глобальный Meter (scope
`github.com/exaring/otelpgx`, не `pgkit`): `db.client.operation.duration`
(гистограмма, unit `s`) и `db.client.operation.errors` (counter).

**PII.** По умолчанию в спан идёт текст запроса (`db.query.text`), но **не**
значения аргументов. Литералы внутри SQL при этом попадают в спан целиком —
персональные данные передавай аргументами (`$1`), а не подстановкой в текст.

Выключение `PG_OTEL_TRACE` убирает все спаны и метрики otelpgx (метрики
`pg_pool_*` от него не зависят и продолжают эмититься).

## Health-пробы

| Проба | `Name` | Проверка |
|---|---|---|
| `pgkit.Probe(db)` | `pgkit` | `SELECT 1` на пуле primary |
| `pgkit.ReplicaProbe(rw)` | `pgkit/replica` | `SELECT 1` на пуле реплики; без реплики проходит без запроса |

Обе регистрируются через `healthkit.ProvideProbe` и попадают в `/ready`; ограничение
времени одной проверки — `healthkit.WithTimeout`.

## Источник правды по реестру

Сводка по всем китам — корневой [README § Что эмитит каждый пакет](../../README.md):
`pgkit` → `pg_pool_*` (9 метрик, label `role`) + спаны `otelpgx`. Эта
страница — детальный реестр имён, типов и labels для `pgkit`.
