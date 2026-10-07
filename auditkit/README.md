# auditkit

Append-only audit-log для сервисов stit: «кто / когда / что сделал над каким
ресурсом». Кит фиксирует действия (`actor_id`, `action`, `resource`,
`resource_id`, `metadata`, `ip`, `request_id`, `at`) в отдельную таблицу, чтобы
compliance- и forensics-запросы не приходилось склеивать из россыпи прикладных
логов. Core-абстракция (`Entry`, `Logger`) отделена от хранилища; единственная
реализация в библиотеке — `pgstore` поверх `pgkit`. Записи хранятся бессрочно, срок
хранения — opt-in `pgstore.RetentionModule` (`AUDIT_RETENTION_DAYS`).

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
`pgkit.MigrateModule` применяет его на старте вместе с миграциями приложения; после них
`Module` сверяет схему (`pgkit.CheckSchemaOnStart`): источник не применён — старт падает с
`pgkit.ErrSchema` и именем источника, а не первая запись аудита. Без fx (`cfg` —
`pgkit.MigrateRunnerConfig`):

```go
err := pgkit.MigrateUpAll(ctx, dsn, cfg.LockTimeout, pgstore.Migrations, pgkit.DirSource(pgkit.AppSourceName, "./migrations"))
```

### Срок хранения

`pgstore.Module` записи не удаляет. Срок хранения включается явно: `RetentionModule` раз в
час удаляет записи старше `AUDIT_RETENTION_DAYS` дней (по `at`, часы базы) батчами по 5000
строк. Ему нужны `timerkit.Module` и `RetentionConfig`:

```go
fx.New(
    // … logkit, pgkit.Module, pgkit.MigrateModule, pgstore.Module — как выше
    fx.Provide(pgstore.NewRetentionConfig), // AUDIT_RETENTION_DAYS — обязателен
    timerkit.Module,
    pgstore.RetentionModule,
)
```

Без fx — `store.DeleteExpired(ctx, pgstore.RetentionConfig{Days: 365})` из своего
планировщика.

## Конфигурация

`pgstore.Store` получает только `*pgkit.DB` (через fx или `NewStore`); подключение к
Postgres настраивается в `pgkit` (`PG_DSN`). Конфиг есть только у срока хранения —
`pgstore.RetentionConfig` (`NewRetentionConfig`), его читает `RetentionModule`:

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `AUDIT_RETENTION_DAYS` | `Days` | `int` | — | возраст записи в днях, после которого её удаляет job retention; обязателен, в `[1, 36500]` (`MaxRetentionDays`) — иначе `auditkit.ErrConfig` на старте |

## Observability

`pgstore.Store` эмитит через глобальные OTel-провайдеры (`otel.Meter`,
scope `git.stit.tech/stit-core/golibinfra/auditkit`); без зарегистрированного
`MeterProvider` инструменты — no-op.

| Метрика | Тип | Labels | Смысл |
|---|---|---|---|
| `audit_log_writes_total` | counter | `result=ok\|error\|invalid` | Исход `Log`: `ok` — строка записана, `error` — `INSERT` не прошёл, `invalid` — запись отвергнута до похода в базу (`Entry.MetadataJSON`: в том числе NUL, не-UTF-8 и `Metadata` сверх предела) |
| `audit_log_write_duration_seconds` | histogram (`s`) | `result=ok\|error` | Длительность `INSERT`; бакеты — общие `otelkit.DurationBuckets` |
| `audit_log_retention_deleted_total` | counter | — | Записи, удалённые job'ом retention (`DeleteExpired`) |

Потребители обычно логируют ошибку `Log` и продолжают доменную операцию, поэтому
`rate(audit_log_writes_total{result!="ok"})` — единственный алертируемый сигнал
потери записи аудита.

Собственных спанов кит не заводит: `Store.Log` и `Store.DeleteExpired` идут через
`*pgkit.DB`, поэтому каждый запрос попадает в per-query span `otelpgx` и в
`pg_pool_*`-метрики `pgkit`; прогон retention — ещё и в спан job'а `timerkit`.

## API

### `auditkit.Entry`

Одна запись аудита. Поля:

| Поле | Тип | Ограничение | Назначение |
|---|---|---|---|
| `ActorID` | `string` | ≤ `MaxActorIDLen` (255) | кто совершил действие (id пользователя/сервиса); пусто → `NULL` |
| `Action` | `string` | обязательно, ≤ `MaxActionLen` (255) | что сделано, напр. `user.create` (`NOT NULL`) |
| `Resource` | `string` | обязательно, ≤ `MaxResourceLen` (255) | над каким типом ресурса, напр. `user` (`NOT NULL`) |
| `ResourceID` | `string` | ≤ `MaxResourceIDLen` (255) | id конкретного ресурса; пусто → `NULL` |
| `Metadata` | `map[string]any` | JSON ≤ `MaxMetadataBytes` (64 КиБ) | произвольный контекст; сериализуется в `JSONB`; пусто → `NULL` |
| `At` | `time.Time` | — | момент события; нулевое значение → `NOW()` базы данных |
| `IP` | `string` | длина не ограничена китом | IP инициатора; пусто → IP клиента из `ctx` (`Enrich`), без него → `NULL` |
| `RequestID` | `string` | длина не ограничена китом | корреляция с HTTP/RPC-запросом; пусто → request-id из `ctx` (`Enrich`), без него → `NULL` |

Все строковые поля — в UTF-8 и без NUL: другое Postgres `TEXT` не хранит. Предел 255
байт у полей-ключей индексов `audit_log` — собственный предел кита, а не Postgres
(индексная запись btree вмещает около 2,7 КиБ): идентификатор или имя действия длиннее —
ошибка вызывающего кода, а не данные события. Ограничения проверяет `Entry.Validate`.

### `auditkit.Entry.Validate`, `MetadataJSON` и `auditkit.ErrInvalidEntry`

```go
func (e Entry) Validate() error
func (e Entry) MetadataJSON() ([]byte, error)
var ErrInvalidEntry = errors.New("auditkit: invalid entry")
const MaxMetadataBytes = 64 << 10
```

`Validate` отвергает то, что хранилище не сохранит или сохранит не тем: пустые `Action` и
`Resource`, значения сверх предельных длин, NUL и не-UTF-8 в строковых полях, `Metadata`
без JSON-представления (канал, `NaN`), длиннее `MaxMetadataBytes` в JSON, с NUL, не-UTF-8
(в значениях и ключах) или одиноким суррогатом (`\ud800` в `json.RawMessage`): `\u0000` и
суррогат Postgres `JSONB` не хранит, а битые байты `encoding/json` молча заменил бы на U+FFFD
(и два ключа слились бы в один). Нарушение — ошибка, обёрнутая `ErrInvalidEntry`
(`errors.Is`), с именем поля, но без значения. `Log` проверяет запись до похода в базу:
пустая или искажённая запись в audit-log хуже ошибки — её никто не заметит, а ошибка
базы на `Log` вне транзакции теряла бы запись под видом сбоя (`result=error`).

`MetadataJSON` делает те же проверки и отдаёт `Metadata` в JSON — то, что пишет
`pgstore` (пустая `Metadata` — `nil`, в базе `NULL`). Сам символ U+FFFD в строке проходит;
escape `\ufffd` в `json.RawMessage` — отказ: так `encoding/json` помечает битые байты. Фейк
`audittest.Recorder` зовёт `Validate` и отвергает те же записи.

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
`e.Enrich(ctx)`, затем `e.MetadataJSON()` (или `Validate`), как `pgstore`.

### `pgstore.Store`

Реализация `Logger` поверх Postgres.

- `NewStore(db *pgkit.DB) *Store` — конструктор.
- `(*Store).Log(ctx, Entry) error` — один `INSERT` в `audit_log`; пустые
  `RequestID` и `IP` — из `ctx` (`Entry.Enrich`). Невалидная
  `Entry` отвергается до запроса (`ErrInvalidEntry`). Пустые строковые поля и
  пустая `Metadata` пишутся как `NULL`; нулевой `At` подставляется как `NOW()`
  на стороне базы (`COALESCE`). Ошибки — с префиксом `auditkit:`.
- `(*Store).DeleteExpired(ctx, RetentionConfig) error` — удаляет записи старше
  `Days` дней по часам базы батчами по 5000 строк до конца отставания; невалидный
  конфиг — `auditkit.ErrConfig` без запроса.

### `pgstore.Module`

`fx.Module("auditkit/pgstore")` — провайдит `*Store` и его же как `auditkit.Logger`,
регистрирует `pgstore.Migrations` в группе `pgkit.migrations` и сверяет схему на старте.
Записи не удаляет.

### `pgstore.RetentionModule`, `RetentionConfig`, `RetentionJob`

```go
var RetentionModule fx.Option // fx.Module("auditkit/pgstore/retention")
type RetentionConfig struct{ Days int } // AUDIT_RETENTION_DAYS
const MaxRetentionDays = 36500
func NewRetentionConfig() (RetentionConfig, error)
func (c RetentionConfig) Validate() error // auditkit.ErrConfig
func RetentionJob(s *Store, cfg RetentionConfig) (timerkit.Job, error)
var ErrConfig = errors.New("auditkit: invalid config") // в пакете auditkit
```

`RetentionModule` кладёт `RetentionJob` (job `auditkit.retention`, раз в час,
`DeleteExpired`) в группу `timerkit.JobsGroup`, после миграций, если в графе
`pgkit.MigrateModule`. Нужны `pgstore.Module`, `RetentionConfig` и `timerkit.Module`:
без него граф не собирается, невалидный конфиг — ошибка сборки графа, а не прогона.

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

`audit_log_at_idx` (по нему идёт и retention) приезжает миграцией
`0002_audit_log_at_idx` и строится `CONCURRENTLY`: на уже наполненной `audit_log`
обычный `CREATE INDEX` держал бы `SHARE`-блокировку и останавливал запись в таблицу на
всё время построения.
Условия, при которых это работает и о которых нужно знать:

- **Файл миграции — ровно один оператор.** Драйвер (`golang-migrate` поверх
  `pgx`) выполняет тело миграции одним `Exec` без явного `BEGIN`, а
  `CONCURRENTLY` внутри транзакционного блока Postgres запрещает; второй
  оператор в том же файле сделал бы блок неявной транзакцией и уронил миграцию.
- **Старт ждёт построения.** `pgkit.MigrateModule` применяет миграции в `OnStart`;
  дедлайн `PG_MIGRATE_TIMEOUT` прерывает построение и оставляет невалидный индекс
  (ниже). На большой таблице стройте индекс заранее вручную (`CREATE INDEX
  CONCURRENTLY IF NOT EXISTS audit_log_at_idx …`) — миграция увидит его и станет no-op.
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
невалидной `Entry` (NUL, не-UTF-8, `Metadata` сверх предела — до базы, `result=invalid`),
граничная `Metadata`, которую база хранит, retention (только записи старше срока, батчи
до конца отставания, метрика), сборка `Module` и `RetentionModule` (`fx.ValidateApp` +
регистрация миграций и job'а), метрики, обратимость миграций (`testkit.MigrationLadder`:
up → down → up по версиям со сверкой схемы).

Тесты, которым нужен Postgres, идут через `testkit.StartPostgres`: гейт
`testkit.RequireDocker` внутри helper'а пропускает их под `go test -short`, а без
Docker роняет. Для unit-тестов бизнес-кода подменяйте `auditkit.Logger` —
готовый in-memory recorder есть в
[`testkit/audittest`](../testkit/audittest/); он валидирует `Entry` так же, как
`pgstore`, хранит копию `Metadata`, а запись в транзакции фейка `testkit/pgtest` её откат
выкидывает.

## Инварианты и подводные камни

- **Core + pgstore split.** Доменный код импортирует только пакет `auditkit`
  (`Entry`, `Logger`); зависимость на Postgres локализована в `pgstore`. Это
  держит бизнес-слой свободным от инфраструктуры и позволяет завести другой
  бэкенд хранения, не трогая вызывающий код.
- **Append-only.** `Log` только пишет (`INSERT`), update не предусмотрен — это
  сознательное свойство audit-log. Удаляет записи только `RetentionModule` — старше
  явно заданного срока; без него аудит хранится бессрочно и растёт с нагрузкой.
  Архивацию до удаления (выгрузку старых записей) делает потребитель.
- **Транзакция берётся из `ctx`.** `Log` пишет в транзакцию, если `ctx` пришёл
  из `pgkit.Transactor.WithTx`: тогда запись атомарна с доменной операцией, и
  возвращённая из `fn` ошибка `Log` откатывает обе (fail-closed). Вне `WithTx`
  `Log` берёт отдельное соединение пула и коммитит сам (autocommit): доменная
  операция уже зафиксирована, и потеря записи аудита остаётся на усмотрение
  приложения (fail-open, сигнал — `audit_log_writes_total{result!="ok"}`).
  Атомарности «сама по себе» синхронность записи не даёт — её даёт только вызов
  внутри `WithTx` с возвратом ошибки наружу.
- **Обязательные поля и вход.** `Action` и `Resource` непусты, поля укладываются в
  предельные длины, строки — в UTF-8 без NUL, `Metadata` — в `MaxMetadataBytes`, иначе
  `Log` возвращает `ErrInvalidEntry` и в базу не идёт. Пользовательский ввод в полях
  аудита (имя, поисковый запрос) проверяйте или нормализуйте до `Log`: отвергнутая запись
  вне транзакции теряется.
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
