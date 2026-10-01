# auditkit

Append-only audit-log для сервисов stit: «кто / когда / что сделал над каким
ресурсом». Кит фиксирует действия (`actor_id`, `action`, `resource`,
`resource_id`, `metadata`, `ip`, `request_id`, `at`) в отдельную таблицу, чтобы
compliance- и forensics-запросы не приходилось склеивать из россыпи прикладных
логов. Core-абстракция (`Entry`, `Logger`) отделена от хранилища; единственная
реализация в библиотеке — `pgstore` поверх `pgkit`.

Место в стеке: бизнес-сервис вызывает `auditkit.Logger.Log` в тех точках, где
произошло значимое изменение состояния (создание/удаление сущности, смена прав,
вход в систему). Запись — синхронная: `Log` пишет в транзакции из `ctx`
(`pgkit.Transactor.WithTx`), если она там есть, иначе автономно, отдельным
соединением пула в autocommit. IP клиента и request-id запроса `Log` берёт из
`ctx` сам (`Entry.Enrich`): их кладут middleware `httpkit` (request-id — и
`grpckit`), сервису заполнять их не нужно.

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
    "context"

    "go.uber.org/fx"

    "git.stit.tech/stit-core/golibinfra/auditkit"
    "git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
    "git.stit.tech/stit-core/golibinfra/logkit"
    "git.stit.tech/stit-core/golibinfra/pgkit"
)

type UserService struct {
    db    *pgkit.DB
    tr    pgkit.Transactor
    audit auditkit.Logger
}

func NewUserService(db *pgkit.DB, tr pgkit.Transactor, audit auditkit.Logger) *UserService {
    return &UserService{db: db, tr: tr, audit: audit}
}

// Запись аудита — в той же транзакции, что и доменное изменение.
func (s *UserService) Create(ctx context.Context, actorID, userID string) error {
    return s.tr.WithTx(ctx, func(ctx context.Context) error {
        if _, err := s.db.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, userID); err != nil {
            return err
        }
        return s.audit.Log(ctx, auditkit.Entry{
            ActorID:    actorID,
            Action:     "user.create",
            Resource:   "user",
            ResourceID: userID,
            Metadata:   map[string]any{"source": "admin-ui"},
            // IP и RequestID — из ctx запроса (httpkit.ClientIP, httpkit.RequestID).
        })
    })
}

func app() *fx.App {
    return fx.New(
        fx.Provide(
            logkit.NewConfig,
            logkit.New,
            pgkit.NewPoolConfig,          // PG_*
            pgkit.NewMigrateRunnerConfig, // PG_MIGRATIONS_PATH, PG_MIGRATE_TIMEOUT
        ),
        pgkit.Module,
        pgkit.MigrateModule, // применяет pgstore.Migrations (+ миграции приложения)
        pgstore.Module,      // provides auditkit.Logger (== *pgstore.Store)

        fx.Provide(NewUserService),
        fx.Invoke(func(*UserService) {}),
    )
}
```

`pgkit.NewReplicaConfig` нужен, только если сервису нужна read-replica —
`pgkit.Module` берёт `ReplicaConfig` опционально.

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

`pgstore.Store` эмитит через глобальные OTel-провайдеры (`otel.Meter`,
scope `git.stit.tech/stit-core/golibinfra/auditkit`); без зарегистрированного
`MeterProvider` инструменты — no-op.

| Метрика | Тип | Labels | Смысл |
|---|---|---|---|
| `audit_log_writes_total` | counter | `result=ok\|error\|invalid` | Исход `Log`: `ok` — строка записана, `error` — `INSERT` не прошёл, `invalid` — запись отвергнута до похода в базу (`Entry.Validate` или несериализуемая `Metadata`) |
| `audit_log_write_duration_seconds` | histogram (`s`) | `result=ok\|error` | Длительность `INSERT`; бакеты — общие `otelkit.DurationBuckets` |

Потребители обычно логируют ошибку `Log` и продолжают доменную операцию, поэтому
`rate(audit_log_writes_total{result!="ok"})` — единственный алертируемый сигнал
потери записи аудита.

Собственных спанов кит не заводит: `Store.Log` выполняет `INSERT` через
`*pgkit.DB`, поэтому каждая запись попадает в per-query span `otelpgx` и в
`pg_pool_*`-метрики `pgkit`.

## API

### `auditkit.Entry`

Одна запись аудита. Поля:

| Поле | Тип | Ограничение | Назначение |
|---|---|---|---|
| `ActorID` | `string` | ≤ `MaxActorIDLen` (255) | кто совершил действие (id пользователя/сервиса); пусто → `NULL` |
| `Action` | `string` | обязательно, ≤ `MaxActionLen` (255) | что сделано, напр. `user.create` (`NOT NULL`) |
| `Resource` | `string` | обязательно, ≤ `MaxResourceLen` (255) | над каким типом ресурса, напр. `user` (`NOT NULL`) |
| `ResourceID` | `string` | ≤ `MaxResourceIDLen` (255) | id конкретного ресурса; пусто → `NULL` |
| `Metadata` | `map[string]any` | длина не ограничена китом | произвольный контекст; сериализуется в `JSONB`; пусто → `NULL` |
| `At` | `time.Time` | — | момент события; нулевое значение → `NOW()` базы данных |
| `IP` | `string` | длина не ограничена китом | IP инициатора; пусто → IP клиента из `ctx` (`Enrich`), без него → `NULL` |
| `RequestID` | `string` | длина не ограничена китом | корреляция с HTTP/RPC-запросом; пусто → request-id из `ctx` (`Enrich`), без него → `NULL` |

Предельные длины — ровно у полей, попадающих в btree-индексы `audit_log`:
значение длиннее не помещается в индексную запись Postgres, и `INSERT` падает
уже в базе. Ограничения проверяет `Entry.Validate`.

### `auditkit.Entry.Validate` и `auditkit.ErrInvalidEntry`

```go
func (e Entry) Validate() error
var ErrInvalidEntry = errors.New("auditkit: invalid entry")
```

`Validate` требует непустых `Action` и `Resource` и соблюдения предельных длин;
нарушение — ошибка, обёрнутая `ErrInvalidEntry` (`errors.Is`). `Log` вызывает её
до похода в базу: пустая запись в audit-log хуже ошибки — её никто не заметит.
Так же ведёт себя фейк `audittest.Recorder`.

### `auditkit.Entry.Enrich`

```go
func (e Entry) Enrich(ctx context.Context) Entry
```

Копия записи, в которой пустые `RequestID` и `IP` взяты из `ctx` запроса: их
кладут middleware `httpkit.RequestID` и `httpkit.ClientIP` (IP — в канонической
форме, тот же, что у лимитера и `bruteforcekit`), request-id — и интерсептор
`grpckit`. Явные значения не перетираются; без них в `ctx` поля остаются пустыми.
`Log` зовёт `Enrich` до `Validate` — и `pgstore.Store`, и `audittest.Recorder`.

### `auditkit.Logger`

```go
type Logger interface {
    Log(ctx context.Context, e Entry) error
}
```

Порт записи аудита. Бизнес-код зависит от этого интерфейса, а не от конкретного
хранилища. Реализуйте свой адаптер, чтобы писать в другой бэкенд: он зовёт
`e.Enrich(ctx)` до `Validate`, как `pgstore`.

### `pgstore.Store`

Реализация `Logger` поверх Postgres.

- `NewStore(db *pgkit.DB) *Store` — конструктор.
- `(*Store).Log(ctx, Entry) error` — один `INSERT` в `audit_log`; пустые
  `RequestID` и `IP` — из `ctx` (`Entry.Enrich`). Невалидная
  `Entry` отвергается до запроса (`ErrInvalidEntry`). Пустые строковые поля и
  пустая `Metadata` пишутся как `NULL`; нулевой `At` подставляется как `NOW()`
  на стороне базы (`COALESCE`). Ошибки — с префиксом `auditkit:`.

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
-- отдельная миграция 0002
CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_log_at_idx ON audit_log (at DESC);
```

Индексы рассчитаны на типовые запросы: «история действий актора», «история по
конкретному ресурсу» и «всё за период» (retention, выгрузки), все — в обратном
хронологическом порядке.

`audit_log_at_idx` приезжает миграцией `0002_audit_log_at_idx` и строится
`CONCURRENTLY`: на уже наполненной `audit_log` обычный `CREATE INDEX` держал бы
`SHARE`-блокировку и останавливал запись в таблицу на всё время построения.
Условия, при которых это работает и о которых нужно знать:

- **Файл миграции — ровно один оператор.** Драйвер (`golang-migrate` поверх
  `pgx`) выполняет тело миграции одним `Exec` без явного `BEGIN`, а
  `CONCURRENTLY` внутри транзакционного блока Postgres запрещает; второй
  оператор в том же файле сделал бы блок неявной транзакцией и уронил миграцию.
- **Старт всё равно ждёт построения.** `pgkit.MigrateModule` применяет миграции
  в `OnStart`, а ограничить время нечем: `PG_MIGRATE_TIMEOUT` доходит только до
  таймаута коннекта, `PG_STATEMENT_TIMEOUT` — параметр пула и на соединение
  миграций не распространяется. На большой таблице стройте индекс заранее
  вручную (`CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_log_at_idx …`) —
  миграция увидит его и станет no-op.
- **Провал `CONCURRENTLY` оставляет невалидный индекс.** Его видно как
  `indisvalid = false` в `pg_index`; повторный прогон миграции из-за
  `IF NOT EXISTS` его не чинит — снимите (`DROP INDEX CONCURRENTLY
  audit_log_at_idx`) и накатите миграцию заново.

## Тестирование

Интеграционный тест поднимает реальный Postgres через
[`testkit.StartPostgres`](../testkit/) (testcontainers) с миграцией кита:

```go
pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
s := pgstore.NewStore(pg.DB(t))
err := s.Log(ctx, auditkit.Entry{Action: "user.create", Resource: "user"})
```

Покрытие: полная запись и `NULL`-поля, `At` из часов базы, участие в транзакции
(`pg.Transactor(t).WithTx` — откат оставляет 0 строк, коммит 1, вне транзакции
строка пишется сразу), ошибка `INSERT` с префиксом `auditkit:`, отказ на
невалидной `Entry`, сборка `Module` (`fx.ValidateApp` + регистрация миграций),
метрики, обратимость миграций (`testkit.MigrationLadder`: up → down → up по
версиям со сверкой схемы).

Тесты, которым нужен Postgres, идут через `testkit.StartPostgres`: гейт
`testkit.RequireDocker` внутри helper'а пропускает их под `go test -short`, а без
Docker роняет. Для unit-тестов бизнес-кода подменяйте `auditkit.Logger` —
готовый in-memory recorder есть в
[`testkit/audittest`](../testkit/audittest/); он валидирует `Entry` так же, как
`pgstore`, и хранит копию `Metadata`.

## Инварианты и подводные камни

- **Core + pgstore split.** Доменный код импортирует только пакет `auditkit`
  (`Entry`, `Logger`); зависимость на Postgres локализована в `pgstore`. Это
  держит бизнес-слой свободным от инфраструктуры и позволяет завести другой
  бэкенд хранения, не трогая вызывающий код.
- **Append-only.** Кит только пишет (`INSERT`). Update/delete не предусмотрены —
  это сознательное свойство audit-log. Ротация/архивация старых записей — забота
  потребителя (партиционирование, retention-джоба по `audit_log_at_idx`), в
  kit'е её нет.
- **Транзакция берётся из `ctx`.** `Log` пишет в транзакцию, если `ctx` пришёл
  из `pgkit.Transactor.WithTx`: тогда запись атомарна с доменной операцией, и
  возвращённая из `fn` ошибка `Log` откатывает обе (fail-closed). Вне `WithTx`
  `Log` берёт отдельное соединение пула и коммитит сам (autocommit): доменная
  операция уже зафиксирована, и потеря записи аудита остаётся на усмотрение
  приложения (fail-open, сигнал — `audit_log_writes_total{result!="ok"}`).
  Атомарности «сама по себе» синхронность записи не даёт — её даёт только вызов
  внутри `WithTx` с возвратом ошибки наружу.
- **Обязательные поля.** `Action` и `Resource` непусты и укладываются в
  предельные длины, иначе `Log` возвращает `ErrInvalidEntry` и в базу не идёт.
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
