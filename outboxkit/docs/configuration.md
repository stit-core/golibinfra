# outboxkit — конфигурация

Конфиг читается из ENV через `envconfig`. **Имена полей и envconfig-теги — part of
public API**: их изменение — breaking change; выпускается minor-релизом,
помечается `!` в Conventional Commit и строкой миграции в аннотации тега
(см. [корневой README § Версионирование](../../README.md)). Ни
`outboxkit.Module`, ни `pgstore.Module` конфиги не provide'ят — caller
регистрирует фабрики: `fx.Provide(outboxkit.NewConfig, pgstore.NewConfig)`.
Невалидное значение валит старт с `outboxkit.ErrConfig`.

## Справочник ENV

`outboxkit.Config` (orchestration-слой, `outboxkit.NewConfig`):

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `OUTBOX_BATCH_SIZE` | `BatchSize` | `int` (`>= 1`) | `100` | Сколько строк `Worker` вычитывает за один тик (`LIMIT`). |
| `OUTBOX_INTERVAL` | `Interval` | `time.Duration` (`> 0`) | `1s` | Период тика `Worker`'а (интервал `timerkit.Job`). |
| `OUTBOX_PUBLISH_MODE` | `PublishMode` | `string` (`batch`\|`ordered`) | `batch` | Режим публикации (см. ниже). Иное значение → `ErrConfig`. |
| `OUTBOX_MAX_ATTEMPTS` | `MaxAttempts` | `int` | `0` | Сколько попыток публикации даётся строке до пометки dead. `<= 0` — без ограничения. |
| `OUTBOX_TICK_TIMEOUT` | `TickTimeout` | `time.Duration` (`> 0`) | `30s` | Дедлайн одного тика: транзакция + публикация. `0` — `ErrConfig`. |
| `OUTBOX_RETRY_BACKOFF` | `RetryBackoff` | `time.Duration` (`>= 0`) | `1s` | Пауза перед первым ретраем строки; каждая следующая неудача удваивает её. `0` — ретрай на ближайшем тике. |
| `OUTBOX_RETRY_MAX_BACKOFF` | `RetryMaxBackoff` | `time.Duration` (`>= RetryBackoff`) | `5m` | Потолок паузы между попытками. `0` — без потолка. |

`pgstore.Config` (Postgres-store, `pgstore.NewConfig`):

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `OUTBOX_RETENTION_MONTHS` | `RetentionMonths` | `int` (`>= 0`) | `0` | Сколько месяцев хранить партиции. `RetentionKeeperJob` дропает старше; `0` — не дропать. |
| `OUTBOX_REQUIRE_TX` | `RequireTx` | `bool` | `true` | `Insert` вне транзакции — `outboxkit.ErrNoTx` (tx-strict insert guard). |

`outboxkit` не имеет собственного эндпоинта брокера/БД. Публикация в JetStream
требует `natskit` с `NATS_JETSTREAM_ENABLED=true` и стримом, покрывающим subject'ы
outbox'а (`NATS_URL`); хранилище — тот же Postgres (версии 14 или новее), что
`PG_DSN` (co-located, см. [architecture.md](architecture.md)).

## Тюнинг publish-mode

- `batch` — максимальная пропускная способность: один round-trip на тик, реплики
  дренируют очередь параллельно. Дефолт; подходит, когда потребители не зависят
  от порядка событий.
- `ordered` — FIFO по очереди ценой round-trip на каждое сообщение, стопа всего
  тика на первой ошибке и одного активного воркера на таблицу (остальные реплики
  пропускают тик, пока голова очереди занята). Брать, только когда порядок
  доставки — жёсткое требование домена; про границы гарантии — раздел «Порядок
  публикации» в [architecture.md](architecture.md).

`OUTBOX_BATCH_SIZE` и `OUTBOX_INTERVAL` балансируют latency и нагрузку: меньший
интервал/больший батч — ниже задержка публикации, выше частота запросов к БД и
брокеру.

## Тюнинг tick-timeout

`OUTBOX_TICK_TIMEOUT` ограничивает выборку и ожидание `PubAck`; на фазу
mark/commit сверху отводится ещё 5 секунд на неотменяемом ctx (см. § Lifecycle в
[architecture.md](architecture.md)). Нулевое значение отвергается на старте: тик
идёт на `context.WithoutCancel`, и «без дедлайна» означало бы транзакцию,
которую не обрывает ни остановка сервиса, ни таймер — только серверные
`statement_timeout`/`idle_in_transaction_session_timeout`. Значение обязано быть **меньше**
`PG_IDLE_IN_TX_SESSION_TIMEOUT` (`pgkit`, default `60s`): во время ожидания
брокера транзакция простаивает, и Postgres оборвёт сессию по этому лимиту.
Таймаут ожидания `PubAck` внутри async-батча задаётся на стороне `natskit`
(`NATS_JS_PUBLISH_ASYNC_TIMEOUT`, потерянный ack → ошибка публикации строки) —
`outboxkit` только передаёт `ctx` с дедлайном тика.

## Тюнинг max-attempts и backoff

- `OUTBOX_MAX_ATTEMPTS = 0` (default) — строка ретраится бесконечно. Уместно,
  когда сбой публикации — это временная недоступность брокера и терять событие
  нельзя.
- `OUTBOX_MAX_ATTEMPTS = N > 0` — после `N` неудачных попыток строка помечается
  dead (`dead_at`), перестаёт вычитываться и не блокирует очередь (важно для
  `ordered`). Эмитится `outbox_events_dead_total`; вернуть строки в очередь —
  `Outbox.ReviveDead` (см. [usage.md](usage.md)).
- Пауза между попытками растёт экспоненциально:
  `OUTBOX_RETRY_BACKOFF × 2^attempts`, но не выше `OUTBOX_RETRY_MAX_BACKOFF`. С
  дефолтами (`1s`, `5m`) десять попыток растянуты примерно на 25 минут, а не на
  10 секунд. Планируя `OUTBOX_MAX_ATTEMPTS`, считайте бюджет ожидания как сумму
  пауз, а не `N × OUTBOX_INTERVAL`.
- `OUTBOX_RETRY_BACKOFF=0` выключает паузу: строка возвращается в выборку
  ближайшим тиком.

## Тюнинг retention

`OUTBOX_RETENTION_MONTHS` управляет `RetentionKeeperJob` (раз в сутки): дропаются
партиции, чей месяц старше `OUTBOX_RETENTION_MONTHS` до текущего и в которых нет
неопубликованных (не-dead) событий; партиции с такими событиями остаются и
перечисляются в `pgstore.ErrPendingRows` (лог `timerkit`). `0` — retention
выключен, партиции живут вечно (растут).

Партиции текущего и следующего месяца создаёт `pgstore.Module` в `OnStart` и
`PartitionKeeperJob` (на старте и раз в сутки), чтобы insert не падал на смене
календаря.

## Миграции и таблицы

Источник `pgstore.Migrations` (`pgkit.Source{Name: "outboxkit"}`, таблица версий
`schema_migrations_outboxkit`) содержит миграции таблицы `outbox`;
`pgstore.Module` регистрирует его в группе `pgkit.MigrateModule`, который
применяет источник на старте до `OnStart`-хука партиций (хук зависит от
`pgkit.Migrated`). Схема таблицы:

```sql
CREATE TABLE outbox (
    id              BIGSERIAL    NOT NULL,
    subject         TEXT         NOT NULL,
    payload         JSONB        NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    published_at    TIMESTAMPTZ,
    attempts        INT          NOT NULL DEFAULT 0,
    last_error      TEXT,
    next_attempt_at TIMESTAMPTZ,
    dead_at         TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
```

Плюс partial-индекс `outbox_unpublished_idx ON (created_at, id) WHERE published_at
IS NULL AND dead_at IS NULL` и партиции на текущий месяц + 3 вперёд.

Колонки:

| Колонка | Назначение |
|---|---|
| `id` | `BIGSERIAL`; уходит как `Nats-Msg-Id` (с namespace в multi-instance). |
| `subject` | NATS-subject публикации; label метрик. |
| `payload` | JSON события (сериализует `outboxkit.Bus`). |
| `created_at` | Время начала транзакции вставки (`NOW()` базы); ключ партиционирования и `ORDER BY`. |
| `published_at` | Время `MarkPublished` (`NOW()` базы); `NULL` = неопубликовано. |
| `attempts` | Счётчик неудачных попыток публикации. |
| `last_error` | Текст последней ошибки (обрезается до 1000 рун). |
| `next_attempt_at` | Момент, раньше которого строка не вычитывается (backoff); `NULL` = готова сейчас. |
| `dead_at` | Время пометки dead; `NULL` = не dead. |

Таблица multi-instance-инстанса заводится тем же набором миграций:
`pgstore.TableMigrations(table)` возвращает `pgkit.Source` для произвольного
имени (таблица версий `schema_migrations_<table>`) — регистрируется как
`pgkit.ProvideMigrations(pgstore.TableMigrations("outbox_orders"))`. DDL целиком
идемпотентен (`IF NOT EXISTS`), поэтому источник применяется и поверх таблицы,
заведённой вручную: недостающие колонки доедут миграцией. Имя таблицы
валидируется (`^[a-z][a-z0-9_]{0,62}$`) — оно инлайнится в SQL и не передаётся
плейсхолдером; для источника миграций имя должно укладываться в 45 символов
(ограничение `pgkit.Source.Name`). `pgstore.SchemaSQL(table)` отдаёт тот же DDL
строкой — для собственной миграции приложения.
