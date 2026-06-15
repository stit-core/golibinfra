# auditkit

Append-only audit-log для сервисов stit: «кто / когда / что сделал над каким
ресурсом». Кит фиксирует действия (`actor_id`, `action`, `resource`,
`resource_id`, `metadata`, `ip`, `request_id`, `at`) в отдельную таблицу, чтобы
compliance- и forensics-запросы не приходилось склеивать из россыпи прикладных
логов. Core-абстракция (`Entry`, `Logger`) отделена от хранилища; единственная
реализация в библиотеке — `pgstore` поверх `pgkit`.

Место в стеке: бизнес-сервис вызывает `auditkit.Logger.Log` в тех точках, где
произошло значимое изменение состояния (создание/удаление сущности, смена прав,
вход в систему). Запись — синхронная, в той же сессии БД, что и доменная
операция.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/auditkit"
import "git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
```

`go get` / версия / `GOPRIVATE` — общие для всей библиотеки, см.
[корневой README](../README.md#доступ-к-репозиторию).

## Быстрый старт

### Через fx

`pgstore.Module` провайдит `*pgstore.Store` как порт `auditkit.Logger`. Зависит
от `*pgkit.DB` — подключайте вместе с `pgkit.Module`.

```go
import (
    "go.uber.org/fx"

    "git.stit.tech/stit-core/golibinfra/auditkit"
    "git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
    "git.stit.tech/stit-core/golibinfra/pgkit"
)

fx.New(
    pgkit.Module,
    pgstore.Module, // provides auditkit.Logger (== *pgstore.Store)

    fx.Invoke(func(audit auditkit.Logger) {
        // в обработчике доменной операции
        _ = audit.Log(ctx, auditkit.Entry{
            ActorID:    "u-42",
            Action:     "user.create",
            Resource:   "user",
            ResourceID: "u-99",
            Metadata:   map[string]any{"source": "admin-ui"},
            IP:         "203.0.113.7",
            RequestID:  "req-abc",
        })
    }),
)
```

### Прямой `New*` (без fx)

```go
store := pgstore.NewStore(db) // db *pgkit.DB
var audit auditkit.Logger = store

err := audit.Log(ctx, auditkit.Entry{Action: "session.login", Resource: "session"})
```

### Проводка миграций

Таблицу `audit_log` создаёт миграция из `pgstore.MigrationsFS` (`embed.FS`,
каталог `migrations`). Подключите её как ещё один источник миграций приложения
через `pgkit`:

```go
import "git.stit.tech/stit-core/golibinfra/pgkit"

err := pgkit.MigrateUpMulti(pgkit.MultiSourceConfig{
    DSN: dsn,
    Sources: []pgkit.Source{
        // ... другие источники
        {Name: "auditkit", FS: pgstore.MigrationsFS, Prefix: "migrations"},
    },
})
```

Порядок `Sources` для прод-БД — append-only: новый источник добавляется в конец
списка (детали — в `pgkit`, тип `pgkit.Source`).

## Конфигурация

Своей `Config` / envconfig-тегов у кита нет. Единственный внешний параметр —
`*pgkit.DB`, который `pgstore.Store` получает в конструкторе (через fx или
`NewStore`). Подключение к Postgres настраивается в `pgkit` (`PG_DSN`).

## Observability

Кит **не эмитит** собственных метрик и спанов. Инструментирование приходит из
нижнего слоя: `Store.Log` выполняет `INSERT` через `*pgkit.DB`, поэтому каждая
запись попадает в per-query span `otelpgx` и в `db_pool_*`-метрики `pgkit`.

## API

### `auditkit.Entry`

Одна запись аудита. Поля:

| Поле | Тип | Назначение |
|---|---|---|
| `ActorID` | `string` | кто совершил действие (id пользователя/сервиса); пусто → `NULL` |
| `Action` | `string` | что сделано, напр. `user.create`; пишется как есть (`NOT NULL`) |
| `Resource` | `string` | над каким типом ресурса, напр. `user` (`NOT NULL`) |
| `ResourceID` | `string` | id конкретного ресурса; пусто → `NULL` |
| `Metadata` | `map[string]any` | произвольный контекст; сериализуется в `JSONB`; пусто → `NULL` |
| `At` | `time.Time` | момент события; нулевое значение → `time.Now()` на стороне `Store` |
| `IP` | `string` | IP инициатора; пусто → `NULL` |
| `RequestID` | `string` | корреляция с HTTP/RPC-запросом; пусто → `NULL` |

### `auditkit.Logger`

```go
type Logger interface {
    Log(ctx context.Context, e Entry) error
}
```

Порт записи аудита. Бизнес-код зависит от этого интерфейса, а не от конкретного
хранилища. Реализуйте свой адаптер, чтобы писать в другой бэкенд.

### `pgstore.Store`

Реализация `Logger` поверх Postgres.

- `NewStore(db *pgkit.DB) *Store` — конструктор.
- `(*Store).Log(ctx, Entry) error` — один `INSERT` в `audit_log`. Пустые
  строковые поля и пустая `Metadata` пишутся как `NULL`; нулевой `At`
  подставляется как `time.Now()`.

### `pgstore.Module`

`fx.Module("auditkit/pgstore")` — провайдит `NewStore` под интерфейсом
`auditkit.Logger` (`fx.As`).

### `pgstore.MigrationsFS`

`embed.FS` с SQL-миграциями (`migrations/0001_audit_log.{up,down}.sql`).
Подключается приложением в общий migrate-runner (см. «Проводка миграций»).

### Схема `audit_log`

```sql
CREATE TABLE audit_log (
    id          BIGSERIAL    PRIMARY KEY,
    actor_id    TEXT,
    action      TEXT         NOT NULL,
    resource    TEXT         NOT NULL,
    resource_id TEXT,
    metadata    JSONB,
    ip          TEXT,
    request_id  TEXT,
    at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX audit_log_actor_idx    ON audit_log (actor_id, at DESC);
CREATE INDEX audit_log_resource_idx ON audit_log (resource, resource_id, at DESC);
```

Индексы рассчитаны на типовые запросы: «история действий актора» и «история по
конкретному ресурсу», обе — в обратном хронологическом порядке.

## Тестирование

Интеграционный тест поднимает реальный Postgres через
[`testkit.StartPostgres`](../testkit/) (testcontainers), скармливая ему
извлечённые из `pgstore.MigrationsFS` миграции:

```go
fx := testkit.StartPostgres(t, migrationsDir) // migrationsDir — распакованный MigrationsFS
db := fx.DB(t)
s := pgstore.NewStore(db)
err := s.Log(ctx, auditkit.Entry{Action: "user.create", Resource: "user"})
```

Тесты помечены `testing.Short()` и пропускаются под `go test -short`. Готового
in-memory фейка кит не поставляет — для unit-тестов бизнес-кода подменяйте
`auditkit.Logger` своим стабом (интерфейс из одного метода).

## Инварианты и подводные камни

- **Core + pgstore split.** Доменный код импортирует только пакет `auditkit`
  (`Entry`, `Logger`); зависимость на Postgres локализована в `pgstore`. Это
  держит бизнес-слой свободным от инфраструктуры и позволяет завести другой
  бэкенд хранения, не трогая вызывающий код.
- **Append-only.** Кит только пишет (`INSERT`). Update/delete не предусмотрены —
  это сознательное свойство audit-log. Ротация/архивация старых записей — забота
  потребителя (партиционирование, retention-джоба), в kit'е её нет.
- **Синхронная запись.** `Log` делает `INSERT` в текущем `ctx`/соединении и
  возвращает ошибку наружу. Решайте на стороне приложения, фатальна ли ошибка
  аудита для доменной операции (писать в одной транзакции или допускать
  расхождение).
- **Что класть в audit-log.** Значимые изменения состояния и действия,
  релевантные для compliance/forensics, — не трассировку и не debug-логи. Для
  них есть `logkit` / спаны. `Metadata` — для структурированного контекста
  события, не для дампа payload'а.
- **Время.** Нулевой `Entry.At` `Store` заменяет на `time.Now()`; колонка —
  `TIMESTAMPTZ`. Для детерминированных тестов передавайте `At` явно.
- **PII.** `actor_id`, `ip`, `metadata` могут содержать персональные данные.
  Redact-фильтры `logkit` сюда не применяются — за минимизацией PII в записях
  следит вызывающий код.
