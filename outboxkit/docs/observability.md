# outboxkit — observability

Единый источник правды по сигналам кита. Meter и инструменты создаются через
`internal/otelkit` под именем `git.stit.tech/stit-core/golibinfra/outboxkit`.
Resource attrs (`service`/`env`/…) и экспорт настраивает потребитель в своей
проводке OTel SDK; без зарегистрированного `MeterProvider` инструменты — no-op.

Все метрики несут label `instance` — имя outbox-инстанса
(`outboxkit.DefaultInstance` = `outbox` у `Module`, `name` у `outboxkit.Instance`
и `outboxkit.New`), чтобы несколько outbox'ов в одном процессе не сливались в
одну серию. Гистограммы `_seconds` используют общие бакеты китов
(`otelkit.DurationBuckets()`, `0.005 … 30`).

## Метрики (всегда)

| Метрика | Тип | Labels | Что считает |
|---|---|---|---|
| `outbox_events_inserted_total` | Int64Counter | `instance`, `subject`, `status` (`ok`\|`error`\|`marshal_error`) | Вставки события в outbox (`Bus.Publish` → `Repository.Insert`). |
| `outbox_events_processed_total` | Int64Counter | `instance`, `subject`, `status` (`published`\|`publish_error`\|`mark_error`) | Исход обработки события `Worker`'ом. |
| `outbox_events_dead_total` | Int64Counter | `instance`, `subject` | Переходы строки в dead (исчерпан `OUTBOX_MAX_ATTEMPTS`). |
| `outbox_revived_total` | Int64Counter | `instance` | Dead-строки, возвращённые в очередь `Outbox.ReviveDead`. |
| `outbox_batch_size` | Int64Histogram | `instance` | Размер батча, вычитанного за тик `Worker`'а. |
| `outbox_batch_duration_seconds` | Float64Histogram (`s`) | `instance` | Длительность всего тика `Worker`'а (включая транзакцию). |
| `outbox_publish_duration_seconds` | Float64Histogram (`s`) | `instance` | Длительность фазы публикации в тике. |

Семантика `status` у `outbox_events_inserted_total`:

- `ok` — событие вставлено в outbox-таблицу.
- `error` — вставка не удалась (ошибка хранилища или tx-strict guard).
- `marshal_error` — payload не сериализуется в JSON; до хранилища не дошёл.

Семантика `status` у `outbox_events_processed_total`:

- `published` — событие подтверждено `PubAck`'ом, помечено published, и тик
  закоммичен (счётчик эмитится после коммита).
- `publish_error` — публикация не удалась (строка уйдёт на ретрай после backoff
  или станет dead).
- `mark_error` — публикация прошла, но `MarkPublished` упал; транзакция тика
  откатилась, строка останется неопубликованной и переотправится (дедуп уберёт
  дубль).

Тик, пропущенный в `ordered`-режиме из-за занятой головы очереди
(`outboxkit.ErrBusy`), в `outbox_batch_size` не попадает — только в
`outbox_batch_duration_seconds`.

## Метрики (через BacklogReporter)

| Метрика | Тип | Labels | Что считает |
|---|---|---|---|
| `outbox_lag_seconds` | Float64ObservableGauge (`s`) | `instance`, `subject` | Возраст самого старого неопубликованного (не-dead) события. |
| `outbox_pending_count` | Int64ObservableGauge | `instance`, `subject` | Количество неопубликованных (не-dead) событий. |
| `outbox_backlog_errors_total` | Int64Counter | `instance` | Снимки backlog'а, завершившиеся ошибкой (gauge'и за этот сбор не эмитятся). |

Эти сигналы появляются, если репозиторий реализует `outboxkit.BacklogReporter`
(`Backlog(ctx) ([]SubjectBacklog, error)`) — его реализует `pgstore.Store` — **и**
callback зарегистрирован:

- **`outboxkit.Module` и `outboxkit.Instance`** — автоматически: fx проверяет,
  реализует ли `Repository` интерфейс. Без reporter'а — no-op. `Unregister`
  вешается на `OnStop`.
- **Вручную** — `Outbox.RegisterBacklogGauges()` (или
  `RegisterBacklogGauges(name, reporter)` для голого `Repository`); сохранить
  вернувшийся `metric.Registration` и вызвать `Unregister()` на shutdown. Метод
  инстанса всегда отдаёт непустую `Registration` — на репозитории без
  `BacklogReporter` это no-op, проверка на `nil` не нужна.

Сбой снимка не валит экспорт метрик процесса: gauge'и за этот сбор просто не
эмитятся, а факт сбоя виден в `outbox_backlog_errors_total` — алерт на lag
должен учитывать этот счётчик, иначе «нет отставания» и «снимок не берётся»
выглядят одинаково.

## Спаны

Сам `outboxkit` спанов не открывает — `Worker` инструментирован только метриками.
Producer-спан публикации создаёт транспорт `natskit.StreamBus` (на
`PublishStream` / `PublishStreamBatch`), куда `Worker` отдаёт сообщения; тик
целиком лежит под спаном `timerkit.job outboxkit.publisher.<instance>`, а запросы
к Postgres получают per-query спаны `otelpgx` от `pgkit`. Trace доставки события
прослеживается через спаны `natskit` (producer) и потребителя.

Label `subject` publish-метрик `natskit` транспорт задаёт равным имени
outbox-инстанса (`StreamMsg.MetricSubject`): доменный subject в этом label дал бы
неограниченную кардинальность.
