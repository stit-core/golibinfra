# outboxkit — тестирование

## Домен: фейковый Publisher

Доменный код зависит от `outboxkit.Publisher`, поэтому его тесты обходятся
`testkit/outboxtest.Recorder` — in-memory реализацией порта с `All`/`Published`/
`Last`/`Count`/`Reset`. Ни Postgres, ни NATS не нужны. Как pgstore с
`OUTBOX_REQUIRE_TX=true`, `Recorder` публикует только в транзакции
(`pgkit.InTx`): вне неё `Publish` отвечает `outboxkit.ErrNoTx` и событие не
копит, поэтому транзактор домена в тесте — фейк `testkit/pgtest`, он помечает
`ctx` транзакцией. Сам `ctx` usecase'а тест не помечает (`pgkit.WithTestTx`):
тогда публикация мимо `Transactor.WithTx` тоже проходит, и тест не видит
ошибку; `pgkit.WithTestTx` — только для прямого вызова `Publish` в тесте.

## Core: fake Repository / Transport

`Worker` зависит только от портов `Repository`, `Transport` и
`pgkit.Transactor`, поэтому его логика тестируется без БД и брокера — на
in-memory фейках (`testkit/pgtest.Identity` / `Recorder` как транзактор;
`pgkit.WithTestTx` снимает tx-strict guard). Так покрываются инварианты:

- тик — ровно одна транзакция (`pgtest.Recorder.Calls() == 1`), режим
  публикации передаётся в `ListUnpublished`, `MarkPublished` получает
  `MessageRef` с ключом партиционирования;
- `batch`: при частичном сбое метятся только подтверждённые строки, неудачные
  идут в failures; `ordered`: стоп на первой ошибке — метится только непрерывный
  префикс подтверждённых, хвост не публикуется;
- `outboxkit.ErrBusy` от репозитория — тихий пропуск тика без публикаций;
- `OUTBOX_TICK_TIMEOUT` виден транспорту как дедлайн `ctx`; отмена ctx во время
  публикации не мешает пометить уже подтверждённые строки (фаза mark/commit идёт
  на `context.WithoutCancel`);
- ошибки хранилища (list / mark / record failures) доходят наружу обёрнутыми;
- poison: при `MaxAttempts > 0` строка, достигшая порога, помечается
  `Failure.Dead`; при `MaxAttempts = 0` dead не наступает никогда; пауза
  `Failure.RetryAfter` растёт экспоненциально и упирается в потолок;
- `last_error` режется по границе руны и остаётся валидным UTF-8;
- zero-value `Config` даёт рабочий воркер (`BatchSize` — дефолт, не `LIMIT 0`);
- счётчики `published`/`dead` эмитятся только после коммита тика, `mark_error` —
  на откате.

Метрики проверяются `sdk/metric.ManualReader` как глобальным `MeterProvider`:
имена, атрибуты (`instance`, `subject`, `status`) и общие бакеты гистограмм.
`fx`-сборки (`outboxkit.Module`, `outboxkit.Instance`) поднимаются через
`fxtest`: резолвятся `Publisher`/`Worker`, в группе `timerkit.Group` лежат
drain- и maintenance-job'ы, backlog-gauge'и появляются на старте и исчезают
после `Unregister` на стопе.

Минимальный фейк-транспорт реализует два метода порта:

```go
func (t *fakeTransport) Publish(ctx context.Context, m outboxkit.OutMsg) error
func (t *fakeTransport) PublishBatch(ctx context.Context, msgs []outboxkit.OutMsg) ([]error, error)
```

`results[i] == nil` означает «i-е сообщение подтверждено».

## Транспорт: дедуп Msg-Id

`Nats-Msg-Id` проверяется на фейковой реализации `natskit.StreamBus`, которая
захватывает опубликованные `StreamMsg`: `NewStreamTransport` отдаёт голый id
(`"42"`), `NewStreamTransportFor(sb, ns)` — namespace'ный (`"account:42"`), один
id в двух namespace'ах даёт разные `Msg-Id` (иначе stream-дедуп уронил бы второй
факт), а `MetricSubject` равен имени инстанса.

## pgstore: testcontainers

Интеграционные тесты `pgstore` поднимают Postgres через
`testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))` — миграции
кита применяются прямо из `embed.FS`, а таблица multi-instance —
`testkit.WithMigrations(pgstore.TableMigrations("outbox_orders"))`. Хелпер сам
пропускает тест под `-short`, а без Docker обрывает его. Покрывают:

- happy-path insert→list→mark (`published_at` ставит база), rollback (строка не
  остаётся в outbox при откате транзакции), tx-strict guard (`ErrNoTx` вне
  транзакции, успех внутри `WithTx` и с маркером `pgkit.WithTestTx`),
  poison+backlog (dead-строка исключается из `ListUnpublished`, `attempts`
  инкрементятся, `Backlog` считает pending), ошибки хранилища на отсутствующей
  таблице;
- порядок выборки: две пересекающиеся транзакции показывают, что очередь идёт
  по началу транзакции вставки, а не по порядку коммитов;
- backoff: строка с `next_attempt_at` в будущем не вычитывается в `batch`, а в
  `ordered` останавливает выборку на голове очереди; наступление срока
  возвращает её;
- `ReviveDead`: фильтр по subject возвращает dead-строки в очередь и сбрасывает
  `attempts`, чужие строки остаются dead;
- обратимость миграций `Migrations` и `TableMigrations` (`testkit.MigrationLadder`:
  up → down → up по версиям, down до нуля и снова up со сверкой схемы);
- партиционирование: `MessageRef` бьёт UPDATE'ами только по своим партициям,
  таблица партиционирована (`relkind = 'p'`), `EnsurePartitions` идемпотентна,
  не падает рядом с транзакцией, создающей ту же партицию, и считает месяцы от
  начала месяца (фиксированные часы `clockkit.NewFixed` на
  31 января);
- retention: старая пустая партиция и партиция только с dead-строками
  отцепляются и дропаются, партиция с неопубликованными остаётся
  (`ErrPendingRows`), а вставки продолжают проходить, пока отцепление ждёт
  завершения открытой транзакции (`DETACH … CONCURRENTLY` не берёт ACCESS
  EXCLUSIVE на родителе);
- блокировки: в `ordered`-режиме вторая транзакция получает `ErrBusy`, пока
  первая держит голову очереди, а `batch` в той же ситуации берёт следующие
  строки;
- два конкурентных `Worker`'а (`worker_test.go`, транспорт-барьер, который
  пускает публикацию только когда в неё вошли оба): в `batch` каждое событие
  публикуется ровно один раз и `attempts` считается один раз на строку; в
  `ordered` второй воркер пропускает тик, пока первый публикует, и итоговый
  порядок — порядок вставки;
- fx: `pgstore.Module` создаёт партиции текущего и следующего месяца в `OnStart`
  и валит старт на невалидном конфиге; `pgstore.FactoryModule` +
  `outboxkit.Instance` поднимают инстанс на кастомной таблице с тремя job'ами и
  включённым tx-guard, а таблицу заводит `pgkit.MigrateModule` из группы;
  `pgstore.Module` и `FactoryModule` + `Instance` работают в одном графе (обе
  схемы накатаны, публикация — каждая в свою таблицу); `TableMigrations`
  доезжает поверх таблицы, заведённой вручную.

Чистые unit-проверки без контейнера: валидация имени таблицы (`NewStoreFor`
отвергает пустое, заглавные, пробелы, SQL-инъекцию), нейминг партиций
(`PartitionSQL` на датах 29–31 числа), `SchemaSQL` в синхроне с миграциями,
`MaintenanceJobs`, `Config.Validate`/`NewConfig` обоих пакетов, отказ
`DropOldPartitions` внутри транзакции.

## Команды

```bash
golangci-lint run ./outboxkit/...
go test -short -race ./outboxkit/...   # без testcontainers
go test -race ./outboxkit/...          # полный прогон (нужен Docker)
```
