# outboxkit — observability

Единый источник правды по сигналам кита. Meter и инструменты создаются под именем
`git.stit.tech/stit-core/golibinfra/outboxkit`. Resource attrs (`service`/`env`/…)
и экспорт настраивает потребитель в своей проводке OTel SDK; без
зарегистрированного `MeterProvider` инструменты — no-op.

## Метрики (всегда)

| Метрика | Тип | Labels | Что считает |
|---|---|---|---|
| `outbox_events_inserted_total` | Int64Counter | `subject`, `status` (`ok`\|`error`) | Вставки события в outbox (`Bus.Publish` → `Repository.Insert`). |
| `outbox_events_processed_total` | Int64Counter | `subject`, `status` (`published`\|`publish_error`\|`mark_error`) | Исход обработки события `Worker`'ом. |
| `outbox_events_dead_total` | Int64Counter | `subject` | Переходы строки в dead (исчерпан `OUTBOX_MAX_ATTEMPTS`). |
| `outbox_batch_size` | Int64Histogram | — | Размер батча, вычитанного за тик `Worker`'а. |
| `outbox_batch_duration_seconds` | Float64Histogram (`s`) | — | Длительность всего тика `Worker`'а. |
| `outbox_publish_duration_seconds` | Float64Histogram (`s`) | — | Длительность фазы публикации в тике. |

Семантика `status` у `outbox_events_processed_total`:

- `published` — событие подтверждено `PubAck`'ом и помечено published.
- `publish_error` — публикация не удалась (строка уйдёт на ретрай или станет dead).
- `mark_error` — публикация прошла, но `MarkPublished` упал; строка останется
  неопубликованной и переотправится (дедуп уберёт дубль).

## Метрики (opt-in через BacklogReporter)

| Метрика | Тип | Labels | Что считает |
|---|---|---|---|
| `outbox_lag_seconds` | Float64ObservableGauge (`s`) | `subject` | Возраст самого старого неопубликованного (не-dead) события. |
| `outbox_pending_count` | Int64ObservableGauge | `subject` | Количество неопубликованных (не-dead) событий. |

Эти два gauge'а эмитятся только если репозиторий реализует
`outboxkit.BacklogReporter` (`Backlog(ctx) ([]SubjectBacklog, error)`) — его
реализует `pgstore.Store` — **и** приложение зарегистрировало callback. Иначе они
просто не появляются.

Регистрация:

- **Single-instance (`outboxkit.Module`)** — автоматически: `fx.Invoke`
  подключает gauge'и, если в графе есть `BacklogReporter` (`pgstore.Store` его
  отдаёт). Без reporter'а — no-op. Unregister вешается на `OnStop`.
- **Multi-instance / вручную** — вызвать `RegisterBacklogGauges(reporter)`,
  сохранить вернувшийся `metric.Registration` и вызвать `Unregister()` на
  shutdown.

Callback устойчив к сбою: если `Backlog` вернул ошибку, она тихо проглатывается —
observability не должна валить процесс.

## Спаны

Сам `outboxkit` спанов не открывает — `Worker` инструментирован только метриками.
Producer-спан публикации создаёт транспорт `natskit.StreamBus` (на
`PublishStream` / `PublishStreamBatch`), куда `Worker` отдаёт сообщения. Trace
доставки события прослеживается через спаны `natskit` (producer) и consumer'а.

## Opt-in сигналы

`outbox_lag_seconds` / `outbox_pending_count` эмитятся только после подключения
`BacklogReporter` (`RegisterBacklogGauges`); остальные `outbox_*` — всегда.
