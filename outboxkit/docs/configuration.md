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
| `OUTBOX_MAX_ATTEMPTS` | `MaxAttempts` | `int` (`>= 1` или `-1`) | `25` | После стольких неудачных публикаций строка помечается dead (с дефолтами backoff — ≈ 1 ч 24 мин, см. ниже). Сбой брокера целиком попытку не тратит. `-1` — без предела, `0` — `ErrConfig`. |
| `OUTBOX_TICK_TIMEOUT` | `TickTimeout` | `time.Duration` (`> 0`) | `30s` | Дедлайн одного тика: транзакция + публикация. `0` — `ErrConfig`. |
| `OUTBOX_RETRY_BACKOFF` | `RetryBackoff` | `time.Duration` (`>= 0`) | `1s` | Пауза перед первым ретраем строки; каждая следующая неудача удваивает её. `0` — ретрай на ближайшем тике. |
| `OUTBOX_RETRY_MAX_BACKOFF` | `RetryMaxBackoff` | `time.Duration` (`>= RetryBackoff`) | `5m` | Потолок паузы между попытками. `0` — без потолка. |
| `OUTBOX_MAX_PAYLOAD_BYTES` | `MaxPayloadBytes` | `int` (`>= 1`) | `524288` | Предел JSON-тела события: больше — `outboxkit.ErrPayloadTooLarge` из `Publish` в транзакции домена. Дефолт — половина `max_payload` NATS (см. ниже). |

`pgstore.Config` (Postgres-store, `pgstore.NewConfig`):

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `OUTBOX_RETENTION_MONTHS` | `RetentionMonths` | `int` (`>= 0`) | `0` | Сколько месяцев хранить партиции. `RetentionKeeperJob` дропает старше; `0` — не дропать. |
| `OUTBOX_REQUIRE_TX` | `RequireTx` | `bool` | `true` | `Insert` вне транзакции — `outboxkit.ErrNoTx` (tx-strict insert guard). |

`outboxkit` не имеет собственного эндпоинта брокера/БД. Публикация в JetStream
требует `natskit` с `NATS_JETSTREAM_ENABLED=true` (выключенный JetStream — ошибка старта
`outboxkit.Module` и `outboxkit.Instance`) и стримом, покрывающим subject'ы outbox'а
(`NATS_URL`; у `Instance` с `WithSubjects` покрытие проверяет старт); хранилище — тот же
Postgres (версии 14 или новее), что `PG_DSN` (co-located, см.
[architecture.md](architecture.md)).

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
(`NATS_JS_PUBLISH_ASYNC_TIMEOUT`; потерянный ack — сбой брокера, попытку строки он не
тратит) — `outboxkit` только передаёт `ctx` с дедлайном тика.

## Тюнинг max-attempts и backoff

- Попытка — неудача публикации **строки**: брокер отверг сообщение, у subject нет стрима.
  После `OUTBOX_MAX_ATTEMPTS` таких неудач (default `25`) строка помечается dead
  (`dead_at`), перестаёт вычитываться и не блокирует очередь (важно для `ordered`). Переход
  виден ERROR-записью `outboxkit: event dead` и `outbox_events_dead_total`; вернуть строки в
  очередь — `Outbox.ReviveDead` (см. [usage.md](usage.md)).
- `OUTBOX_MAX_ATTEMPTS=-1` — без предела: строка не становится dead никогда. Это политика
  «не терять и не переставлять» для `ordered`: poison держит очередь, пока причину не починят,
  и виден по `publish_error`, `outbox_lag_seconds` и ERROR `timerkit: job error` каждого тика.
- Сбой брокера целиком — соединение, JetStream, выключенный на сервере или в рантайме, таймаут
  `PubAck`, дедлайн тика — попытку не тратит: строка остаётся без паузы и уходит в следующий
  тик, в метрике — `publish_error`. Простой брокера любой длины не уводит очередь в dead.
- Пауза между попытками растёт экспоненциально:
  `OUTBOX_RETRY_BACKOFF × 2^attempts`, но не выше `OUTBOX_RETRY_MAX_BACKOFF`. Бюджет — сумма
  пауз между попытками, а не `N × OUTBOX_INTERVAL`: с дефолтами (`1s`, `5m`, `25`) это 24
  паузы — 1 + 2 + … + 256 = 511 с у первых девяти и 15 × 300 с — ≈ 1 ч 24 мин от первой
  неудачи до dead (десять попыток — 9 пауз, 8 мин 31 с). Пауза отсчитывается от начала
  транзакции тика (`NOW()`), строку берёт первый тик после неё — к каждой паузе добавляется
  до `OUTBOX_INTERVAL`.
- `OUTBOX_RETRY_BACKOFF=0` выключает паузу: строка возвращается в выборку
  ближайшим тиком.

## Тюнинг payload

`OUTBOX_MAX_PAYLOAD_BYTES` меряет JSON, который сериализует `Bus`: событие сверх предела
`Publish` отвергает в транзакции домена (`ErrPayloadTooLarge`), а не оставляет строкой, которую
брокер не примет никогда. Воркер публикует строку в представлении хранилища: JSONB `pgstore`
длиннее JSON на пробел после каждого `:` и `,` — до 1,5 раза у массива однозначных чисел, — а
к телу идут заголовки (`Nats-Msg-Id`, trace). Поэтому дефолт — половина `max_payload` NATS
(1 MiB у сервера по умолчанию); поднимая предел, держите его не выше половины `max_payload`
сервера.

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
имени (таблица версий `schema_migrations_<table>`); `outboxkit.Instance` кладёт
его в группу `pgkit.migrations` сам, и `pgkit.MigrateModule` накатывает его на
старте. DDL целиком идемпотентен (`IF NOT EXISTS`), поэтому источник применяется
и поверх таблицы, заведённой вручную: недостающие колонки доедут миграцией, а
откат такой миграции схему версии 1 (это уже вся схема `SchemaSQL`) не трогает. Имя
таблицы валидируется (`^[a-z][a-z0-9_]{0,62}$`) — оно инлайнится в SQL и не
передаётся плейсхолдером; для источника миграций имя должно укладываться в 45
символов (ограничение `pgkit.Source.Name`). `pgstore.SchemaSQL(table)` отдаёт
тот же DDL строкой — для собственной миграции приложения.
