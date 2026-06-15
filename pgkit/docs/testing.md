# pgkit — тестирование

Интеграционные тесты поднимают реальный Postgres в контейнере через
`testkit.StartPostgres`. Unit-уровень (`errors.go`, `ReadWriteDB`-роутинг)
тестируется без контейнера.

## `testkit.StartPostgres`

```go
func StartPostgres(t *testing.T, migrationsPath string) *PostgresFixture
```

Поднимает `postgres:17-alpine` на свободном порту, ждёт готовности и регистрирует
`t.Cleanup` для остановки контейнера. Если `migrationsPath != ""` — сразу
прогоняет `pgkit.MigrateUp`.

```go
type PostgresFixture struct {
    DSN       string
    Container *tcpostgres.PostgresContainer
}

func (f *PostgresFixture) DB(t *testing.T) *pgkit.DB
```

`Fixture.DB(t)` открывает `pgxpool` к контейнеру, оборачивает в `*pgkit.DB` и
регистрирует закрытие пула в `t.Cleanup`.

## Рецепт: пул с реальной БД

```go
func TestRepo(t *testing.T) {
    if testing.Short() {
        t.Skip("integration")
    }
    pg := testkit.StartPostgres(t, "")
    cfg := pgkit.PoolConfig{
        DSN:      pg.DSN,
        MaxConns: 2, MinConns: 1,
        MaxConnLifetime: 5 * time.Minute,
        MaxConnIdleTime: time.Minute,
        HealthCheck:     30 * time.Second,
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

`MigrateUpMulti` принимает любой `fs.FS`, поэтому источник можно собрать через
`fstest.MapFS` — без файлов на диске:

```go
src := fstest.MapFS{
    "migrations/0001_foo.up.sql":   {Data: []byte(`CREATE TABLE foo (id INT PRIMARY KEY);`)},
    "migrations/0001_foo.down.sql": {Data: []byte(`DROP TABLE foo;`)},
}
pg := testkit.StartPostgres(t, "")
cfg := pgkit.MultiSourceConfig{
    DSN:     pg.DSN,
    Sources: []pgkit.Source{{Name: "src", FS: src, Prefix: "migrations"}},
}
if err := pgkit.MigrateUpMulti(cfg); err != nil {
    t.Fatalf("MigrateUpMulti: %v", err)
}
```

`MigrateVersionMulti` после этого вернёт число применённых миграций;
`MigrateDownMulti` откатит один шаг.

## Рецепт: проверка метрик пула

`InstrumentPool` цепляется к любому `metric.Meter`; в тесте подойдёт
`sdkmetric.NewManualReader`:

```go
reader := sdkmetric.NewManualReader()
mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
reg, err := pgkit.InstrumentPool(pool, mp.Meter("pgkit-test"))
// ...
defer reg.Unregister()

var rm metricdata.ResourceMetrics
_ = reader.Collect(context.Background(), &rm) // → db_pool_* в rm.ScopeMetrics
```

## Запуск

```bash
go test -short -race ./...   # unit: контейнер-тесты помечены testing.Short() и скипаются
go test -race ./...          # integration: поднимает Postgres (нужен Docker)
```
