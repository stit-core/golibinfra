# outboxkit — конфигурация

Конфиг читается из ENV через `envconfig`. **Имена полей и envconfig-теги — part of
public API**: их изменение — breaking change и идёт мажорным апгрейдом. Ни
`outboxkit.Module`, ни `pgstore.Module` конфиги не provide'ят — caller
регистрирует фабрики: `fx.Provide(outboxkit.NewConfig, pgstore.NewConfig)`.

## Справочник ENV

`outboxkit.Config` (orchestration-слой, `outboxkit.NewConfig`; `Validate()`
отвергает невалидные значения):

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `OUTBOX_BATCH_SIZE` | `BatchSize` | `int` (`>= 1`) | `100` | Сколько строк `Worker` вычитывает за один тик (`LIMIT`). |
| `OUTBOX_INTERVAL` | `Interval` | `time.Duration` (`> 0`) | `1s` | Период тика `Worker`'а (интервал `timerkit.Job`). |
| `OUTBOX_PUBLISH_MODE` | `PublishMode` | `string` (`batch`\|`ordered`) | `batch` | Режим публикации (см. ниже). Иное значение → `NewConfig` возвращает ошибку. |
| `OUTBOX_MAX_ATTEMPTS` | `MaxAttempts` | `int` | `0` | Порог попыток до пометки строки dead. `<= 0` — без ограничения (ретрай вечно). |
| `OUTBOX_TICK_TIMEOUT` | `TickTimeout` | `time.Duration` (`>= 0`) | `30s` | Дедлайн одного тика: транзакция + публикация. `0` — без дедлайна. |

`pgstore.Config` (Postgres-store, `pgstore.NewConfig`):

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `OUTBOX_RETENTION_MONTHS` | `RetentionMonths` | `int` | `0` | Сколько месяцев хранить партиции. `RetentionKeeperJob` дропает старше; `0` — не дропать. |
| `OUTBOX_REQUIRE_TX` | `RequireTx` | `bool` | `false` | `true` → `Insert` вне транзакции — ошибка (tx-strict insert guard). |

`outboxkit` не имеет собственного эндпоинта брокера/БД. Публикация в JetStream
требует `natskit` с `NATS_JETSTREAM_ENABLED=true` и стримом, покрывающим subject'ы
outbox'а (`NATS_URL`); хранилище — тот же Postgres, что `PG_DSN` (co-located, см.
[architecture.md](architecture.md)).

## Тюнинг publish-mode

- `batch` — максимальная пропускная способность: один round-trip на тик, порядок
  персиста best-effort, реплики дренируют очередь параллельно. Дефолт; подходит,
  когда consumer'ы не зависят от строгого порядка событий.
- `ordered` — строгий порядок per-subject ценой round-trip на каждое сообщение,
  стопа всего тика на первой ошибке и одного активного воркера на таблицу
  (остальные реплики пропускают тик, пока голова очереди занята). Брать, только
  когда порядок доставки — жёсткое требование домена.

`OUTBOX_BATCH_SIZE` и `OUTBOX_INTERVAL` балансируют latency и нагрузку: меньший
интервал/больший батч — ниже задержка публикации, выше частота запросов к БД и
брокеру.

## Тюнинг tick-timeout

`OUTBOX_TICK_TIMEOUT` ограничивает тик целиком: выборку, ожидание `PubAck` и
mark. По истечении публикация прерывается (`ctx`), транзакция откатывается,
строки уходят на следующий тик. Значение обязано быть **меньше**
`PG_IDLE_IN_TX_SESSION_TIMEOUT` (`pgkit`, default `60s`): во время ожидания
брокера транзакция простаивает, и Postgres оборвёт сессию по этому лимиту.
Таймаут ожидания `PubAck` внутри async-батча задаётся на стороне `natskit`
(`NATS_JS_PUBLISH_ASYNC_TIMEOUT`, потерянный ack → ошибка публикации строки) —
`outboxkit` только передаёт `ctx` с дедлайном тика.

## Тюнинг max-attempts

- `0` (default) — строка ретраится бесконечно. Уместно, когда сбой публикации —
  это временная недоступность брокера, и терять событие нельзя.
- `N > 0` — после `N` неудачных попыток строка помечается dead (`dead_at`),
  перестаёт вычитываться и не блокирует очередь (важно для `ordered`). Dead-строки
  остаются в таблице с `attempts`/`last_error` для разбора до retention. Эмитится
  `outbox_events_dead_total`.

## Тюнинг retention

`OUTBOX_RETENTION_MONTHS` управляет `RetentionKeeperJob` (раз в сутки): дропаются
партиции, чей месяц старше `OUTBOX_RETENTION_MONTHS` до текущего и в которых нет
неопубликованных (не-dead) событий; партиции с такими событиями остаются и
перечисляются в `pgstore.ErrPendingRows` (лог `timerkit`). `0` — retention
выключен, партиции живут вечно (растут).

Партиции текущего и следующего месяца создаёт `pgstore.Module` в `OnStart` и
`PartitionKeeperJob` (раз в сутки), чтобы insert не падал на смене календаря.

## Миграции и таблицы

Embedded-миграция `pgstore/migrations/0001_outbox.up.sql` — источник
`pgstore.Migrations` (`pgkit.Source{Name: "outboxkit"}`, таблица версий
`schema_migrations_outboxkit`); `pgstore.Module` регистрирует его в группе
`pgkit.MigrateModule`, который применяет его на старте до `OnStart`-хука
партиций (`pgkit.MigrateModule` в `fx.New` перечисляется раньше
`pgstore.Module`). Миграция создаёт single-instance таблицу:

```sql
CREATE TABLE outbox (
    id           BIGSERIAL    NOT NULL,
    subject      TEXT         NOT NULL,
    payload      JSONB        NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ,
    attempts     INT          NOT NULL DEFAULT 0,
    last_error   TEXT,
    dead_at      TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
```

Плюс partial-индекс `outbox_unpublished_idx ON (created_at, id) WHERE published_at
IS NULL AND dead_at IS NULL` и партиции на текущий месяц + 3 вперёд. Down-миграция
дропает таблицу.

Колонки:

| Колонка | Назначение |
|---|---|
| `id` | `BIGSERIAL`; уходит как `Nats-Msg-Id` (с namespace в multi-instance). |
| `subject` | NATS-subject публикации; label метрик. |
| `payload` | JSON-сериализованный `payload` из `Publish` (`json.Marshal`). |
| `created_at` | Время вставки (`NOW()` базы); ключ партиционирования и `ORDER BY`. |
| `published_at` | Время `MarkPublished` (`NOW()` базы); `NULL` = неопубликовано. |
| `attempts` | Счётчик неудачных попыток публикации. |
| `last_error` | Текст последней ошибки (обрезается до 1000 символов). |
| `dead_at` | Время пометки dead; `NULL` = не dead. |

Таблица для multi-instance заводится caller'ом: `pgstore.SchemaSQL(table)`
возвращает идентичный DDL (`CREATE TABLE IF NOT EXISTS` + индекс) для произвольного
имени, партиции — `EnsurePartitions`. Имя таблицы валидируется
(`^[a-z][a-z0-9_]{0,62}$`) — оно инлайнится в SQL и не передаётся плейсхолдером.
