# pgkit — observability

Единый источник правды по телеметрии kit'а. Пул инструментируется метриками
`db_pool_*`, а соединения — per-query спанами через `otelpgx`. Метрики
регистрирует `InstrumentPool(pool, meter)`; в fx-графе это делает
`instrumentPoolFx`, привязывая отписку (`Registration.Unregister`) к `OnStop`.

Kit **не выставляет** resource-атрибуты
(`service.name`/`service.version`/`deployment.environment.name`/`tenant`) — они
приходят из `telemetry.Config` приложения и корп `otel-collector`. Labels
автоматически дополняются resource-атрибутами на коллекторе.

## Метрики `db_pool_*`

Девять метрик, все снимаются в одном `RegisterCallback` поверх `pool.Stat()`.
Gauge'и отражают мгновенное состояние пула, counter'ы — кумулятивные счётчики
pgxpool.

| Имя | Тип | Источник (`pgxpool.Stat`) | Назначение |
|---|---|---|---|
| `db_pool_acquired_conns` | Int64ObservableGauge | `AcquiredConns()` | соединения, занятые прямо сейчас |
| `db_pool_idle_conns` | Int64ObservableGauge | `IdleConns()` | простаивающие соединения |
| `db_pool_total_conns` | Int64ObservableGauge | `TotalConns()` | всего открытых соединений |
| `db_pool_max_conns` | Int64ObservableGauge | `MaxConns()` | сконфигурированный лимит пула |
| `db_pool_constructing_conns` | Int64ObservableGauge | `ConstructingConns()` | соединения в процессе установки |
| `db_pool_empty_acquire_total` | Int64ObservableCounter | `EmptyAcquireCount()` | acquire'ы, ждавшие из-за пустого пула |
| `db_pool_canceled_acquire_total` | Int64ObservableCounter | `CanceledAcquireCount()` | acquire'ы, отменённые по `ctx` |
| `db_pool_acquire_total` | Int64ObservableCounter | `AcquireCount()` | всего успешных acquire'ов |
| `db_pool_acquire_duration_seconds_total` | Float64ObservableCounter (unit `s`) | `AcquireDuration().Seconds()` | суммарное время ожидания acquire |

### Labels

Все метрики **attribute-less**, per-pool — без labels на уровне kit'а. Если в
приложении несколько пулов в одном OTel scope, их значения сложатся. Различай
пулы через resource-атрибуты или регистрируй каждый под собственным meter-scope.

### Чтение

- **Сатурация пула.** `db_pool_acquired_conns / db_pool_max_conns` близко к `1`
  плюс растущий `db_pool_empty_acquire_total` — пул упирается в лимит; повышай
  `PG_MAX_CONNS` (сверяясь с `max_connections` сервера) или снижай длительность
  запросов.
- **Ожидание acquire.** Рост `db_pool_acquire_duration_seconds_total` относительно
  `db_pool_acquire_total` (средняя задержка получения соединения) — тот же
  симптом нехватки соединений.
- **Отмены.** `db_pool_canceled_acquire_total` растёт — запросы отменяются по
  таймауту/`ctx` ещё на стадии ожидания соединения (обычно следствие сатурации).

## Спаны (`otelpgx`)

При `PG_OTEL_TRACE=true` (default) на `ConnConfig.Tracer` ставится
`otelpgx.NewTracer()`. Он создаёт **per-query span** на каждый
`Exec`/`Query`/`QueryRow`/batch, с SQL-атрибутами по семантическим конвенциям
OTel БД. Спаны наследуют контекст вызова через `ctx`, встраиваясь в общую
трассу запроса.

Выключение `PG_OTEL_TRACE` убирает per-query спаны (метрики `db_pool_*` от него
не зависят и продолжают эмититься).

## Источник правды по реестру

Сводка по всем китам — корневой [README § Что эмитит каждый пакет](../../README.md):
`pgkit` → `db_pool_*` (9 метрик) + per-query span (`otelpgx`). Эта страница —
детальный реестр имён, типов и labels для `pgkit`.
