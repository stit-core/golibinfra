# outboxkit — архитектура

## Назначение

`outboxkit` реализует паттерн transactional outbox: запись факта в БД и его
последующая публикация в брокер связаны одним атомарным коммитом. Это снимает
проблему dual-write — ситуации, когда доменный state и сообщение в брокере
расходятся, потому что одна из двух записей прошла, а вторая упала.

Поток:

1. Доменный код пишет state и вызывает `Publisher.Publish(ctx, subject, payload)`
   внутри **одной** транзакции (`pgkit` `Transactor.WithTx`). `Bus` через
   `Repository.Insert` кладёт событие в outbox-таблицу. Коммит транзакции делает
   и доменную запись, и outbox-строку видимыми атомарно; rollback убирает обе.
2. `Worker` отдельным фоновым тиком (`timerkit.Job`, интервал `OUTBOX_INTERVAL`)
   открывает транзакцию, вычитывает и запирает неопубликованные строки
   (`Repository.ListUnpublished`), публикует их в JetStream через `Transport`,
   помечает published (`Repository.MarkPublished`) только подтверждённые
   `PubAck`'ом, фиксирует неудачи (`Repository.RecordFailures`) и коммитит.

## Слои и абстракции

Core-абстракция отделена от Postgres-реализации, чтобы доменный код зависел от
портов, а не от БД.

- `outboxkit.Repository` — core-порт хранилища (`Insert`, `ListUnpublished`,
  `MarkPublished`, `RecordFailures`).
- `outboxkit.Transport` — порт публикации в брокер (`Publish`, `PublishBatch`).
- `outboxkit.Publisher` — узкий порт публикации для доменов и тестов (один метод
  `Publish`), реализуется `Bus`.
- `pgkit.Transactor` — граница атомарности тика `Worker`'а; для не-Postgres
  хранилища подойдёт любая реализация интерфейса.
- `outboxkit.Bus` — пишет событие в `Repository` (insert-сторона).
- `outboxkit.Worker` — drain-сторона: тик = транзакция list → publish → mark /
  record-failures.
- `outboxkit/pgstore.Store` — Postgres-реализация `Repository` (и
  `BacklogReporter`).
- `streamTransport` (`NewStreamTransport` / `NewStreamTransportFor`) — адаптер
  `natskit.StreamBus` под `Transport`.

`outboxkit.Outbox` (через `New`) связывает `Bus` + `Worker` + конфиг в один
именованный инстанс и отдаёт `Publisher()` и `Job()` — для multi-instance.

## Гарантии доставки

Гарантия — **at-least-once**. Контракт `Transport`: возврат без ошибки — только
после подтверждённого персиста (`JetStream PubAck`). Если `Worker` не получил
подтверждение, строка не метится published и переотправляется следующим тиком.

Дубли на ретрае гасит дедуп стрима: id строки уходит как `Nats-Msg-Id`, и в
пределах окна `StreamConfig.Duplicates` JetStream отбрасывает повтор с тем же id.
Это даёт effectively-once персист в стриме при ретраях продьюсера.

Если `MarkPublished` падает после успешной публикации — транзакция тика
откатывается, ошибка идёт наружу, строки остаются неопубликованными и
переотправятся; дедуп уберёт второй факт из стрима.

## Транзакционный тик и несколько реплик

Тик `Worker`'а — одна транзакция `pgkit.Transactor.WithTx` с дедлайном
`OUTBOX_TICK_TIMEOUT`. `ListUnpublished` выбирает строки `FOR UPDATE`, и
блокировки живут до коммита — пока воркер публикует, никакая другая реплика эти
строки не видит как свободные.

- `batch` — `FOR UPDATE SKIP LOCKED`: реплики берут непересекающиеся наборы строк
  и дренируют очередь параллельно. Ни одна строка не публикуется двумя воркерами
  одновременно, `attempts` инкрементится ровно один раз на неудачу.
- `ordered` — `FOR UPDATE NOWAIT` по голове очереди: если первые строки заперты
  другой репликой, Postgres отвечает `55P03`, `pgstore` превращает его в
  `outboxkit.ErrBusy`, а `Worker` молча пропускает тик. В каждый момент очередь
  дренирует ровно один воркер, порядок публикации — `(created_at, id)`, стоп на
  первой ошибке.

Ошибка публикации **не** откатывает транзакцию: mark подтверждённых и запись
неудач коммитятся, ошибка возвращается после коммита (иначе poison-строка в
батче никогда не давала бы пометить соседей). Откат — только на ошибке
хранилища (list / mark / record failures).

Пока воркер ждёт `PubAck`, транзакция простаивает — `OUTBOX_TICK_TIMEOUT`
обязан быть меньше `PG_IDLE_IN_TX_SESSION_TIMEOUT` (`pgkit`), иначе Postgres
оборвёт сессию раньше, чем тик завершится.

Порядок `(created_at, id)` — порядок коммитов вставок, а не начала транзакций:
событие из долгой транзакции, закоммиченной позже, публикуется позже соседей с
более поздним `created_at`. Строгий порядок гарантирован для событий, чьи
вставки закоммичены последовательно.

## Режимы публикации

`OUTBOX_PUBLISH_MODE` выбирает компромисс «порядок vs пропускная способность» на
хопе worker→брокер:

- `batch` (default) — async-батч: все строки тика улетают разом одним
  round-trip'ом, метятся published только подтверждённые `PubAck`'ом;
  неподтверждённые уходят на ретрай. Порядок персиста — best-effort (при частичном
  сбое неподтверждённое сообщение может получить более поздний sequence; реплики
  дренируют очередь параллельно). Дедуп по `MsgID` гарантирует отсутствие дублей.
- `ordered` — последовательная публикация со стопом на первой ошибке: published
  метится только непрерывный префикс подтверждённых. Более позднее событие не
  персистится прежде неудавшегося — строгий порядок per-subject ценой round-trip
  на каждое сообщение и одного активного воркера на таблицу.

## Poison → dead

Строка, которую брокер стабильно отвергает (poison), не должна вечно крутиться в
очереди и в `ordered`-режиме блокировать её хвост.

При неудачной публикации `Worker` вызывает `Repository.RecordFailures`, который
инкрементит `attempts` и пишет `last_error`. Если `OUTBOX_MAX_ATTEMPTS > 0` и
очередная попытка достигает порога (`Attempts + 1 >= MaxAttempts`), строка
помечается `Failure.Dead = true` → в Postgres проставляется `dead_at = NOW()`.
Dead-строки больше не вычитываются `ListUnpublished` (фильтр `dead_at IS NULL`) —
они освобождают очередь, но остаются в таблице для разбора (`last_error`,
`attempts`) до retention партиции. При каждом переходе в dead эмитится
`outbox_events_dead_total`.

`OUTBOX_MAX_ATTEMPTS = 0` (default) — без ограничения: строка ретраится вечно.
Подходит, когда сбой публикации означает временную недоступность брокера, а не
неисправимое сообщение.

## Партиционирование и retention

`pgstore`-таблица партиционирована `PARTITION BY RANGE (created_at)` по месяцам.
Это даёт дешёвый retention: удалить старые опубликованные события — это `DROP`
целой партиции (`DropOldPartitions`), а не `DELETE` строк.

- `EnsurePartitions(ctx, 1)` создаёт партиции текущего и следующего месяца.
  `pgstore.Module` вызывает её в `OnStart` (сервис, перезапускаемый чаще раза в
  сутки, иначе никогда не дожил бы до первого тика keeper'а) и раз в сутки через
  `PartitionKeeperJob`. Идемпотентна.
- `RetentionKeeperJob` (раз в сутки) — `DropOldPartitions(ctx, OUTBOX_RETENTION_MONTHS)`:
  дропает партиции, чей месяц старше `OUTBOX_RETENTION_MONTHS` до текущего.
  Партиция с неопубликованными (не-dead) событиями остаётся; такие партиции
  перечисляются в `pgstore.ErrPendingRows`, который `timerkit` логирует — это
  сигнал разобраться с застрявшими событиями. Партиции только с опубликованными
  и dead-строками дропаются. `0` (default) — ничего не дропать.

Embedded-миграция `0001_outbox.up.sql` создаёт таблицу `outbox`, partial-индекс по
неопубликованным и партиции на текущий месяц + 3 вперёд; `pgstore.Migrations` —
источник для `pgkit.MigrateModule` (`pgstore.Module` регистрирует его сам).
Кастомные таблицы (multi-instance) bootstrap'ит caller через `SchemaSQL(table)` +
`EnsurePartitions`.

## Single-instance vs multi-instance

**Single-instance** — одна таблица `outbox`, fx-проводка через `pgstore.Module` +
`outboxkit.Module`; имя инстанса `outboxkit.DefaultInstance` (`outbox`) — label
`instance` в метриках. `Transport` — `NewStreamTransport` (без namespace): голый
id строки идёт как `Nats-Msg-Id`. Корректно, потому что `BIGSERIAL` одной
таблицы глобально уникален.

**Multi-instance** — несколько независимых outbox'ов в одном приложении (по
таблице на домен), собранных вручную: `outboxkit.New(name, store, transport,
transactor, cfg)` + `pgstore.NewStoreFor(db, table)` на каждый. Каждая
per-domain таблица имеет собственный независимый `BIGSERIAL` — id повторяются
между таблицами. `name` становится label'ом `instance`.

## Namespaced Nats-Msg-Id

Stream-дедуп идёт по `Nats-Msg-Id`, **не** по subject. Если несколько
outbox-таблиц делят один JetStream-стрим, голый id столкнётся: строка `id=1` из
таблицы A и `id=1` из таблицы B получат одинаковый `Msg-Id`, и дедуп молча уронит
второй факт.

Решение — `NewStreamTransportFor(sb, namespace)`: он формирует
`Nats-Msg-Id = "<namespace>:<id>"`. namespace обычно равен имени `Outbox`-инстанса
(`outboxkit.New`). Per-row дедуп ретраев сохраняется (та же строка → тот же
`Msg-Id`), а cross-table коллизии исчезают.

## tx-strict insert guard

`pgstore.WithRequireTx()` включает проверку: `Insert` вне активной транзакции
(`pgkit.InTx(ctx) == false`) возвращает ошибку. Это защищает главный инвариант
outbox — событие должно писаться атомарно с доменным state одной транзакцией, а не
отдельным коннектом из пула (иначе теряется смысл паттерна: запись факта и
доменный коммит снова разъезжаются). Включается через `OUTBOX_REQUIRE_TX=true`
(single-instance `pgstore.Module`) или опцией `WithRequireTx()` (вручную).

## Ключевые решения (WHY)

- **Core отделён от pgstore.** Доменный код зависит от `Repository`/`Publisher`,
  а не от Postgres — другая реализация хранилища не трогает домен.
- **Тик — транзакция, а не claim-колонки.** Блокировки строк живут ровно
  столько, сколько тик: падение воркера освобождает их откатом, без TTL и
  «протухших» claim'ов; схема таблицы не растёт.
- **`MarkPublished` батчем после `PubAck`.** Метятся только подтверждённые строки
  — потеря между publish и mark безопасна (переотправка + дедуп).
- **Id строки = `Nats-Msg-Id`.** Бесплатная идемпотентность переотправки: стрим
  сам гасит дубль.
- **Партиции по месяцам.** Retention через `DROP` партиции, не построчный
  `DELETE`; партиция с неопубликованным не дропается.
- **`dead_at` вместо удаления poison.** Освобождает очередь, но сохраняет строку
  для разбора причины.
- **co-located с pg-хостом.** У `outboxkit` нет своего per-concern эндпоинта: он
  пишется в той же транзакции, что и доменные данные, значит живёт на том же
  Postgres, что `PG_DSN` (см. корневой `README.md` § Per-concern endpoints).
