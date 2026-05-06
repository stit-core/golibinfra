# testkit

Хелперы для интеграционных тестов.

## StartPostgres

```go
func TestRepository_Create(t *testing.T) {
    fx := testkit.StartPostgres(t, "../../../../migrations")
    pool, _ := pgkit.NewPool(context.Background(), pgkit.PoolConfig{DSN: fx.DSN, MaxConns: 4})
    defer pool.Close()

    repo := postgres.NewRepository(pool)
    // ... assertions
}
```

Контейнер автоматически останавливается через `t.Cleanup`. Миграции накатываются перед возвратом — тесты получают полностью готовую схему.
