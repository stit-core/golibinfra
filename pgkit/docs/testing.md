# pgkit — тестирование

Интеграционные тесты поднимают реальный Postgres в контейнере через
`testkit.StartPostgres`. Unit-уровень (`errors.go`, `ReadWriteDB`-роутинг,
валидация источников миграций, таймаут коннекта) тестируется без контейнера.

## `testkit.StartPostgres`

```go
func StartPostgres(t testing.TB, opts ...Option) *PostgresFixture

func WithMigrations(sources ...pgkit.Source) Option // источники китов/приложения
func WithMigrationsDir(path string) Option          // каталог приложения (источник app)
func WithImage(ref string) Option                   // другой образ (pgvector, postgis)
```

Поднимает `testkit.PostgresImage` на свободном порту, ждёт готовности и
регистрирует `t.Cleanup` для остановки контейнера. Под `-short` сам делает
`t.Skip`, без Docker — `t.Fatalf`. Переданные источники применяются через
`pgkit.MigrateUpAll` — так же, как это делает `MigrateModule`.

```go
type PostgresFixture struct {
    DSN       string
    Container *tcpostgres.PostgresContainer
}

func (f *PostgresFixture) Pool(t testing.TB) *pgxpool.Pool
func (f *PostgresFixture) DB(t testing.TB) *pgkit.DB
func (f *PostgresFixture) Transactor(t testing.TB) pgkit.Transactor
func (f *PostgresFixture) PoolConfig() pgkit.PoolConfig
```

`Pool`, `DB` и `Transactor` работают поверх одного пула фикстуры, открытого
через `pgkit.NewPool` с prod-дефолтами (session-таймауты, otelpgx); пул
закрывается в `t.Cleanup` теста, стартовавшего контейнер.

Обратимость своих миграций проверяет `testkit.MigrationLadder(t, opts...)` с теми
же опциями: каждая версия — up → down → up со сверкой схемы (down возвращает её к
состоянию до своего up), затем down до нуля и снова up.

## Рецепт: пул с реальной БД

```go
func TestRepo(t *testing.T) {
    pg := testkit.StartPostgres(t, testkit.WithMigrationsDir("testdata/migrations"))
    cfg := pgkit.PoolConfig{
        DSN:      pg.DSN,
        MaxConns: 2, MinConns: 1,
        MaxConnLifetime: 5 * time.Minute,
        MaxConnIdleTime: time.Minute,
        HealthCheck:     30 * time.Second,
        ConnectTimeout:  10 * time.Second,
        StatementTimeout: 7 * time.Second,
    }
    pool, err := pgkit.NewPool(context.Background(), cfg)
    if err != nil {
        t.Fatalf("NewPool: %v", err)
    }
    t.Cleanup(pool.Close)
    // ...
}
```

## Рецепт: миграции из in-memory FS

`Source` принимает любой `fs.FS`, поэтому источник можно собрать через
`fstest.MapFS` — без файлов на диске:

```go
src := pgkit.Source{Name: "src", Dir: "migrations", FS: fstest.MapFS{
    "migrations/0001_foo.up.sql":   {Data: []byte(`CREATE TABLE foo (id INT PRIMARY KEY);`)},
    "migrations/0001_foo.down.sql": {Data: []byte(`DROP TABLE foo;`)},
}}
pg := testkit.StartPostgres(t, testkit.WithMigrations(src))
// или вручную (lock timeout: чужих сессий у контейнера теста нет):
err := pgkit.MigrateUp(ctx, pg.DSN, time.Minute, src)
v, dirty, err := pgkit.MigrateVersion(ctx, pg.DSN, src) // 1, false
err = pgkit.MigrateDown(ctx, pg.DSN, time.Minute, src)  // один шаг назад
```

Регрессионный сценарий кита (`migrate_test.go`): к источнику добавляется
файл `0002`, `MigrateUpAll` применяет его ровно один раз, версия приложения в
`schema_migrations_app` не меняется. Там же — ALTER под чужой блокировкой (lock timeout
отпускает читателей таблицы) и дедлайн, прерывающий идущий оператор и оператор, начатый
после дедлайна между миграциями. Проверку схемы хранилища кита (`CheckSchema`,
`CheckSchemaOnStart`) — `schema_test.go`: без миграций старт падает с `ErrSchema`.

## Рецепт: проверка метрик пула

`InstrumentPool` пишет в глобальный Meter — тест читает его через провайдер
процесса `testkit/metrictest` (его ставит первый `Collect`; статистику пула
инструменты читают в момент снимка):

```go
reg := pgkit.InstrumentPool(pool, pgkit.RolePrimary)
defer reg.Unregister()

snap := metrictest.Collect(t)
_ = snap.Find("pg_pool_total_conns", "role", pgkit.RolePrimary) // серии pg_pool_*{role="primary"}
```

## Рецепт: домен без Postgres

Фейки `pgkit.Transactor` живут в `testkit/pgtest`: `pgtest.Identity` прогоняет
`fn` в транзакции фейка, `pgtest.Recorder` ещё и считает вызовы. Транзакция фейка кладёт в
`ctx` маркер `pgkit.WithTestTx`, поэтому код, гейтящийся на `pgkit.InTx` (например
`outboxkit/pgstore` с `WithRequireTx`), проходит и без БД; ошибка `fn` откатывает записи
фейков `outboxtest` и `audittest`:

```go
svc := NewService(repo, pgtest.Identity{})
if err := svc.Do(ctx); err != nil { /* ... */ }
```

## Запуск

```bash
go test -short -race ./...   # unit: контейнер-тесты скипает testkit.RequireDocker
go test -race ./...          # integration: поднимает Postgres (нужен Docker)
```
