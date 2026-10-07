# outboxkit — observability

Единый источник правды по сигналам кита. Meter и инструменты создаются через
`internal/otelkit` под именем `git.stit.tech/stit-core/golibinfra/outboxkit`.
Resource attrs (`service`/`env`/…) и экспорт настраивает потребитель в своей
проводке OTel SDK; без зарегистрированного `MeterProvider` инструменты — no-op.

Все метрики несут label `instance` — имя outbox-инстанса
(`outboxkit.DefaultInstance` = `outbox` у `Module`, `name` у `outboxkit.Instance`
и `outboxkit.New`), чтобы несколько outbox'ов в одном процессе не сливались в
одну серию. Label `subject` есть только у инстанса с `outboxkit.WithMetricSubject(fn)`:
его значение — шаблон, в который `fn` сворачивает subject события
(`order.42.created` → `order.*.created`). Subject — тип события, id — в payload: subject с id в
label дал бы серию на сущность. Гистограммы `_seconds` используют общие бакеты китов
(`otelkit.DurationBuckets()`, `0.005 … 30`).

## Метрики (всегда)

| Метрика | Тип | Labels | Что считает |
|---|---|---|---|
| `outbox_events_inserted_total` | Int64Counter | `instance`, `subject`¹, `status` (`ok`\|`error`\|`marshal_error`\|`payload_too_large`\|`undeclared_subject`) | Вставки события в outbox (`Bus.Publish` → `Repository.Insert`). |
| `outbox_events_processed_total` | Int64Counter | `instance`, `subject`¹, `status` (`published`\|`publish_error`\|`mark_error`) | Исход обработки события `Worker`'ом. |
| `outbox_events_dead_total` | Int64Counter | `instance`, `subject`¹ | Переходы строки в dead (исчерпан `OUTBOX_MAX_ATTEMPTS`). |
| `outbox_revived_total` | Int64Counter | `instance` | Dead-строки, возвращённые в очередь `Outbox.ReviveDead`. |
| `outbox_batch_size` | Int64Histogram | `instance` | Размер батча, вычитанного за тик `Worker`'а. |
| `outbox_batch_duration_seconds` | Float64Histogram (`s`) | `instance` | Длительность всего тика `Worker`'а (включая транзакцию). |
| `outbox_publish_duration_seconds` | Float64Histogram (`s`) | `instance` | Длительность фазы публикации в тике. |

¹ — только с `WithMetricSubject` инстанса, значение — шаблон.

Семантика `status` у `outbox_events_inserted_total`:

- `ok` — событие вставлено в outbox-таблицу.
- `error` — вставка не удалась (ошибка хранилища или tx-strict guard).
- `marshal_error` — payload не сериализуется в JSON; до хранилища не дошёл.
- `payload_too_large` — тело в представлении хранилища больше `OUTBOX_MAX_PAYLOAD_BYTES`
  (`outboxkit.ErrPayloadTooLarge` из `Repository.Insert`); строка не записана.
- `undeclared_subject` — subject вне `WithSubjects` инстанса
  (`outboxkit.ErrSubjectNotDeclared`); до хранилища не дошёл.

Семантика `status` у `outbox_events_processed_total`:

- `published` — событие подтверждено `PubAck`'ом, помечено published, и тик
  закоммичен (счётчик эмитится после коммита).
- `publish_error` — публикация не удалась: неудача строки уходит на ретрай после backoff
  или в dead, сбой брокера целиком (`outboxkit.ErrUnavailable`, батч без результатов) —
  на следующий тик без траты попытки.
- `mark_error` — публикация прошла, но `MarkPublished` упал; транзакция тика
  откатилась, строка останется неопубликованной и переотправится (дедуп уберёт
  дубль).

Тик, пропущенный в `ordered`-режиме из-за занятой головы очереди
(`outboxkit.ErrBusy`), в `outbox_batch_size` не попадает — только в
`outbox_batch_duration_seconds`.

## Метрики (через BacklogReporter)

| Метрика | Тип | Labels | Что считает |
|---|---|---|---|
| `outbox_lag_seconds` | Float64ObservableGauge (`s`) | `instance`, `subject`¹ | Возраст самого старого неопубликованного (не-dead) события инстанса (по шаблону — самого старого из его subject'ов). |
| `outbox_pending_count` | Int64ObservableGauge | `instance`, `subject`¹ | Количество неопубликованных (не-dead) событий инстанса (по шаблону — сумма). |
| `outbox_backlog_errors_total` | Int64Counter | `instance` | Снимки backlog'а, завершившиеся ошибкой (gauge'и за этот сбор не эмитятся). |

Серия инстанса без шаблона есть и у пустой очереди — с нулями; по шаблону серии есть только у
шаблонов с неопубликованными событиями.

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

## Логи

`Worker` пишет в `logkit.Logger` (у `Module` и `Instance` — из графа, у `New` и `NewWorker` —
аргументом) одну запись — переход строки в dead, после коммита тика:

| Уровень | Сообщение | Поля |
|---|---|---|
| ERROR | `outboxkit: event dead` | `instance`, `id`, `subject`, `attempts`, `error` |

Событие больше не доставится — без telemetry `outbox_events_dead_total` — no-op, и запись
лога остаётся единственным сигналом. Прочие ошибки тика (публикация, хранилище) `Worker.Run`
возвращает: их пишет `timerkit` (`timerkit: job error`). Выключенный JetStream и subject'ы
`WithSubjects` без стрима — ошибка старта, а не запись лога.

## Спаны и трасса события

Сам `outboxkit` спанов не открывает — `Worker` инструментирован только метриками.
Producer-спан публикации создаёт транспорт `natskit.StreamBus` (на
`PublishStream` / `PublishStreamBatch`), куда `Worker` отдаёт сообщения; тик
целиком лежит под спаном `timerkit.job outboxkit.publisher.<instance>`, а запросы
к Postgres получают per-query спаны `otelpgx` от `pgkit`.

Трасса запроса продолжается через outbox: `pgstore` хранит в строке W3C trace-context
(`traceparent`, `tracestate`; без baggage) span'а из ctx `Publish`, и producer-спан публикации —
ребёнок этого контекста со ссылкой (link) на спан тика (`natskit.WithTraceParent`); потребитель
события оказывается в трассе запроса, а решение о сэмплинге — запроса. Строка без
контекста публикуется в трассе тика.

Шаблон из `WithMetricSubject` уходит и в сигналы `natskit`: label `subject` publish-метрик, имя
producer-спана и `messaging.destination.template`; без опции их нет — спан называется
`natskit.publish`, реальный subject — в `messaging.destination.name`.
