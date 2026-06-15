# outboxkit — тестирование

## Core: fake Repository / Transport

`Worker` зависит только от портов `Repository` и `Transport`, поэтому его логика
тестируется без БД и брокера — на in-memory фейках. Так покрываются ключевые
инварианты:

- `batch`: при частичном сбое метятся только подтверждённые строки, неудачные
  идут в failures.
- `ordered`: стоп на первой ошибке — метится только непрерывный префикс
  подтверждённых, хвост не публикуется.
- mark-error: публикация прошла, `MarkPublished` упал → ошибка наружу, ни одна
  строка не помечена (переотправится).
- poison: при `MaxAttempts > 0` строка, достигшая порога, помечается
  `Failure.Dead`; при `MaxAttempts = 0` dead не наступает никогда.
- batch-level error (например, JetStream disabled) → ничего не помечено.

Минимальный фейк-транспорт реализует два метода порта:

```go
func (t *fakeTransport) Publish(ctx context.Context, m outboxkit.OutMsg) error
func (t *fakeTransport) PublishBatch(ctx context.Context, msgs []outboxkit.OutMsg) ([]error, error)
```

`results[i] == nil` означает «i-е сообщение подтверждено».

## Транспорт: дедуп Msg-Id

`Nats-Msg-Id` проверяется на фейковой реализации `natskit.StreamBus`, которая
захватывает опубликованные `StreamMsg`: `NewStreamTransport` отдаёт голый id
(`"42"`), `NewStreamTransportFor(sb, ns)` — namespace'ный (`"account:42"`), и один
id в двух namespace'ах обязан давать разные `Msg-Id` (иначе stream-дедуп уронил бы
второй факт).

## pgstore: testcontainers

Интеграционные тесты `pgstore` поднимают Postgres через
`testkit.StartPostgres(t, migDir)` и прогоняют embedded-миграции
(`pgstore.MigrationsFS` распаковывается во временную папку). Они помечены
`if testing.Short() { t.Skip(...) }` — в `go test -short` пропускаются, в полном
прогоне с `testkit` запускают контейнер. Покрывают: happy-path insert→list→mark,
rollback (строка не остаётся в outbox при откате транзакции), tx-strict guard
(`WithRequireTx` отвергает insert вне tx, принимает внутри `WithTx`), poison+backlog
(dead-строка исключается из `ListUnpublished`, `attempts` инкрементятся, `Backlog`
считает pending), кастомную таблицу (`NewStoreFor` + `SchemaSQL` +
`EnsurePartitions`), retention (`DropOldPartitions` удаляет старую партицию,
сохраняя текущую), партиционированность таблицы (`relkind = 'p'`).

Чистые unit-проверки без контейнера (бегут и в `-short`): валидация имени таблицы
`NewStoreFor` (отвергает пустое, заглавные, пробелы, SQL-инъекцию), нейминг
партиций `PartitionSQL`, непустота `SchemaSQL`, отказ tx-strict guard на `nil`-db.

## Команды

```bash
golangci-lint run ./...
go test -short -race ./...   # без testcontainers
go test -race ./...          # полный прогон (нужен Docker для testkit)
go build ./...
```
