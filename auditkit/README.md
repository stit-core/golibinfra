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

`pgstore.Module` провайдит `*pgstore.Store` как порт `auditkit.Logger` и
регистрирует миграцию кита в группе `pgkit.MigrateModule`. Зависит от
`*pgkit.DB` — подключайте вместе с `pgkit.Module`.

```go
import (
    "go.uber.org/fx"

    "git.stit.tech/stit-core/golibinfra/auditkit"
    "git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
    "git.stit.tech/stit-core/golibinfra/pgkit"
)

fx.New(
    pgkit.Module,
    pgkit.MigrateModule, // применяет pgstore.Migrations (+ миграции приложения)
    pgstore.Module,      // provides auditkit.Logger (== *pgstore.Store)

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

Таблицу `audit_log` создают миграции источника `pgstore.Migrations`
(`pgkit.Source{Name: "auditkit"}`, таблица версий `schema_migrations_auditkit`).
`pgstore.Module` кладёт его в fx-группу `pgkit.migrations`, и
`pgkit.MigrateModule` применяет его на старте вместе с миграциями приложения.
Без fx:

```go
err := pgkit.MigrateUpAll(ctx, dsn, pgstore.Migrations, pgkit.DirSource(pgkit.AppSourceName, "./migrations"))
```

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
| `At` | `time.Time` | момент события; нулевое значение → `NOW()` базы данных |
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
  подставляется как `NOW()` на стороне базы (`COALESCE`). Ошибки — с префиксом
  `auditkit:`.

### `pgstore.Module`

`fx.Module("auditkit/pgstore")` — провайдит `NewStore` под интерфейсом
`auditkit.Logger` (`fx.As`) и регистрирует `pgstore.Migrations` в группе
`pgkit.migrations`.

### `pgstore.Migrations`

`pgkit.Source` с SQL-миграциями кита (`migrations/*.sql`, embed). Подключается
`pgkit.MigrateModule` автоматически через `pgstore.Module` (см. «Проводка
миграций»).

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
CREATE INDEX audit_log_at_idx       ON audit_log (at DESC);
```

Индексы рассчитаны на типовые запросы: «история действий актора», «история по
конкретному ресурсу» и «всё за период» (retention, выгрузки), все — в обратном
хронологическом порядке.

## Тестирование

Интеграционный тест поднимает реальный Postgres через
[`testkit.StartPostgres`](../testkit/) (testcontainers) с миграцией кита:

```go
fx := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
db := fx.DB(t)
s := pgstore.NewStore(db)
err := s.Log(ctx, auditkit.Entry{Action: "user.create", Resource: "user"})
```

Тесты помечены `testing.Short()` и пропускаются под `go test -short`. Для
unit-тестов бизнес-кода подменяйте `auditkit.Logger` — готовый in-memory
recorder есть в [`testkit/audittest`](../testkit/audittest/).

## Инварианты и подводные камни

- **Core + pgstore split.** Доменный код импортирует только пакет `auditkit`
  (`Entry`, `Logger`); зависимость на Postgres локализована в `pgstore`. Это
  держит бизнес-слой свободным от инфраструктуры и позволяет завести другой
  бэкенд хранения, не трогая вызывающий код.
- **Append-only.** Кит только пишет (`INSERT`). Update/delete не предусмотрены —
  это сознательное свойство audit-log. Ротация/архивация старых записей — забота
  потребителя (партиционирование, retention-джоба по `audit_log_at_idx`), в
  kit'е её нет.
- **Синхронная запись.** `Log` делает `INSERT` в текущем `ctx`/соединении и
  возвращает ошибку наружу. Решайте на стороне приложения, фатальна ли ошибка
  аудита для доменной операции (писать в одной транзакции или допускать
  расхождение).
- **Что класть в audit-log.** Значимые изменения состояния и действия,
  релевантные для compliance/forensics, — не трассировку и не debug-логи. Для
  них есть `logkit` / спаны. `Metadata` — для структурированного контекста
  события, не для дампа payload'а.
- **Время.** Нулевой `Entry.At` заменяется на `NOW()` базы — единые часы для всех
  записей независимо от часов инстансов сервиса; колонка — `TIMESTAMPTZ`. Для
  детерминированных тестов передавайте `At` явно.
- **PII.** `actor_id`, `ip`, `metadata` могут содержать персональные данные.
  Redact-фильтры `logkit` сюда не применяются — за минимизацией PII в записях
  следит вызывающий код.
