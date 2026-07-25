# Архитектура natskit

## Назначение и место в стеке

`natskit` — транспортный слой поверх NATS для сервисов stit. Он покрывает две
модели доставки и прячет их за минимальной поверхностью:

1. **Core NATS** (`Bus`) — fire-and-forget pub/sub без персиста. Подходит для
   эфемерных уведомлений, где допустима потеря при отсутствии живого подписчика.
   Подписка бывает fan-out (`Subscribe` — каждому инстансу) и load-balanced
   (`SubscribeQueue` — одному из queue-группы).
2. **JetStream** (`StreamBus`) — персист + at-least-once доставка через
   durable-консьюмеры с explicit-ack, дедуп публикации по `Nats-Msg-Id`,
   poison-обработка (DLQ / `MaxDeliver` / NAK-backoff). Подходит для событий,
   которые нельзя терять.

Поверх обеих моделей кит даёт: типизированные JSON-helper'ы (дженерики),
panic recovery в обработчиках, backpressure через семафор, W3C trace-propagation
и метрики `nats_*`.

Доменное — вне кита: схемы сообщений, идемпотентность по бизнес-ключу,
 retry-политика бизнес-уровня, выбор subject'ов и stream'ов. `natskit` отвечает
за доставку байтов и надёжность транспорта, не за смысл сообщений.

## Компоненты

| Файл | Роль |
|---|---|
| `bus.go` | `Bus`-интерфейс (`Publish`/`Subscribe`/`SubscribeQueue`), `natsBus` (Core NATS), дженерики `PublishJSON[T]`/`SubscribeJSON[T]`/`SubscribeQueueJSON[T]`, `decodeJSON`. |
| `jetstream.go` | `StreamBus`-интерфейс, `jsBus` (JetStream), `disabledStreamBus` (no-op при выключенном JS), `ensureStreams`, poison-обработка (`failStream`/`terminal`/`deadLetter`), `StreamMsg`, `WithMsgID`, дженерики `PublishStreamJSON[T]`/`SubscribeStreamJSON[T]`. |
| `conn.go` | `Conn` — обёртка над `*nats.Conn`; `NewConn`, `Healthy`, `Close` (drain + close). |
| `client.go` | `Client` — инстанс шины (соединение + `Bus` + `StreamBus`), `New`, `Probe` (health-probe инстанса). |
| `config.go` | `Config` / `BackpressureConfig` / `JetStreamConfig` — публичный env-контракт; `NewConfig`, `NewConfigPrefixed`. |
| `env.go` | Чтение конфига по тегам `envconfig` с префиксом инстанса — без fallback на беспрефиксные переменные. |
| `context.go` | `SubjectFromContext` / `HeadersFromContext` — доступ к subject и header'ам входящего сообщения внутри обработчика. |
| `instrumentation.go` | `natsInstruments` (5 otel-инструментов), producer/consumer-спаны, `natsHeaderCarrier` (TextMapCarrier на `nats.Header` для trace-propagation). |
| `module.go` | fx `Module(name)` (Config + `*Client` + `Bus` + `StreamBus`), `HealthProbe(name)`. |

## Bus (Core NATS)

`natsBus` оборачивает `*nats.Conn`. Поток:

```
Publish(ctx, subject, payload any)
   │  json.Marshal(payload)
   │  startPublishSpan: producer-span + inject trace-context в nats.Header
   ▼
conn.PublishMsg(msg) ──► nats_messages_published_total{subject,status}
                         nats_publish_duration_seconds{subject}

Subscribe / SubscribeQueue
   │  callback на каждое входящее сообщение
   ▼
sem.Acquire(1) ──► go handleOne(...)   (backpressure: не более MaxInFlight)
   │  inflight +1
   │  startConsumerSpan: extract trace-context → child-span; subject+headers в ctx
   ▼
handler(ctx, msg.Data)
   │  panic → recover → status=panic (сообщение не доставляется повторно)
   │  err   → status=handler_error (логируется, кроме context.Canceled)
   ▼
nats_messages_consumed_total{subject,status}; nats_handle_duration_seconds; inflight -1
```

`Subscribe` vs `SubscribeQueue` — разница только в NATS-примитиве: `Subscribe`
делает fan-out (каждый инстанс получает копию), `SubscribeQueue` —
`QueueSubscribe` (сообщение получает ровно один инстанс из именованной
queue-группы; так горизонтально масштабируются Core-консьюмеры).

Core NATS не подтверждает доставку: при handler-ошибке или panic сообщение
**не** переотправляется — оно уже сконсюмлено. Нужна гарантия доставки и
ретраи — это JetStream.

## StreamBus (JetStream)

`jsBus` активен только при `NATS_JETSTREAM_ENABLED=true`; иначе `NewStreamBus`
возвращает `disabledStreamBus`, у которого все методы отдают
`ErrJetStreamDisabled` (no-op, не nil — приложение не падает при сборке графа,
а получает явную ошибку при попытке использовать выключенный транспорт).

### Декларация стримов

При старте `ensureStreams` создаёт/обновляет стримы из `NATS_JS_STREAM_NAMES`.
Каждый стрим: subjects `<name>.>`, file-storage, retention/`MaxAge`/`Duplicates`
из конфига. `Duplicates` поджимается до `MaxAge`, если задан больше (требование
JetStream: окно дедупа ≤ срок хранения). Существующий стрим обновляется
(`UpdateStream`), а не падает.

### Публикация

- `PublishStream` — синхронно: `PublishMsg` + ждёт `PubAck`.
- `PublishStreamBatch` — async: все сообщения уходят `PublishMsgAsync`, затем
  ожидается `PublishAsyncComplete`, после чего собираются per-message результаты
  (`results[i] == nil` — персист подтверждён). Внешняя ошибка — сбой всего батча
  (JS выключен / отмена `ctx`). Порядок персиста best-effort: при частичном сбое
  неподтверждённые уходят на ретрай и могут получить более поздний sequence —
  дедуп по `MsgID` гарантирует отсутствие дублей.

`WithMsgID(id)` (или `StreamMsg.MsgID`) ставит `Nats-Msg-Id`: в пределах окна
`Duplicates` повторная публикация с тем же id дедуплицируется стримом
(effectively-once персист при ретраях продьюсера).

### Consumer reliability

`SubscribeStream(stream, subject, durable, handler)` создаёт/обновляет durable
pull-консьюмера с `AckExplicitPolicy`, `FilterSubject=subject`,
`AckWait=NATS_JS_ACK_WAIT`, `MaxAckPending=NATS_JS_MAX_IN_FLIGHT`. Обработка
каждого сообщения (`handleStreamOne`):

```
sem.Acquire(1)  (если не удалось — Nak, сообщение вернётся позже)
   │ inflight +1
   ▼
invoke(handler): panic → recover (panicked=true), иначе вернуть ошибку handler'а
   │
   ├─ успех ───────────────► msg.Ack()          status=ok
   └─ ошибка/panic ────────► failStream(...)
                                │
           NumDelivered ≥ MaxDeliver (terminal)?
                ├─ да  → deadLetter (если DLQSubject задан) + msg.Term()   status=dead
                └─ нет → NakWithDelay(NakBackoff) | Nak()  status=handler_error|panic
```

- **`MaxDeliver`** — потолок числа доставок. Ставится и на консьюмере
  (server-side backstop), и проверяется клиентом перед `Term`. `MaxDeliver ≤ 0`
  — без лимита (бесконечный ределивери).
- **NAK-backoff** — при `NakBackoff > 0` неуспешное сообщение возвращается с
  задержкой (`NakWithDelay`), иначе мгновенно (`Nak`). Задержка распределяет
  ретраи во времени, не упирая консьюмера в горячую петлю.
- **DLQ** — на исчерпании попыток сообщение перед `Term` republish'ится в
  `DLQSubject` (Core-публикацией, best-effort) с заголовками `Natskit-Dlq-Origin`
  (исходный subject) и `Natskit-Dlq-Error` (текст ошибки), плюс все исходные
  header'ы. `Term` останавливает дальнейший ределивери. Без `DLQSubject` —
  только `Term` (тихий drop после исчерпания).

`durable` — имя durable-консьюмера: переподключение продолжает с того же места,
а не с начала стрима. Несколько инстансов с одним `durable` делят нагрузку
(JetStream-аналог queue-группы).

## Trace-propagation

W3C trace-context (`traceparent`/`tracestate`) ездит вместе с сообщением в
`nats.Header` через `natsHeaderCarrier` (TextMapCarrier поверх `nats.Header`):

- Producer (`startPublishSpan`): открывает span kind=`producer`, инжектит
  контекст в header'ы публикуемого `nats.Msg`.
- Consumer (`startConsumerSpan`): экстрактит контекст из header'ов входящего
  сообщения и стартует span kind=`consumer` как child producer-спана — даже
  через персист JetStream трасса остаётся связной.

`SubjectFromContext(ctx)` / `HeadersFromContext(ctx)` дают обработчику доступ к
конкретному subject (для wildcard-подписок вроде `orders.*`) и к заголовкам
(`event-type`, `schema-version` и пр.) — без необходимости дублировать
их в payload.

## Backpressure

И `natsBus`, и `jsBus` держат `semaphore.Weighted`. Каждое входящее сообщение
обрабатывается в отдельной горутине, но не больше `MaxInFlight` одновременно:

- Core (`natsBus`): не удалось взять семафор → сообщение логируется и
  пропускается (Core не подтверждает — повтора нет).
- JetStream (`jsBus`): не удалось взять семафор → `Nak` (сообщение вернётся
  позже). Дополнительно `MaxAckPending` на консьюмере ограничивает число
  неподтверждённых сообщений, что JetStream доставит.

Семафор защищает сервис от перегрузки при всплеске трафика; одновременная
конкуренция обработчиков ограничена, остальное ждёт/откладывается.

## fx Module

`Module(name)` provide'ит `Config`, `*Client`, `Bus` и `StreamBus` одного
инстанса и вешает `OnStop`-хук с `Drain()` + `Close()` — graceful-разрыв на
shutdown.

Пустое имя — дефолтный инстанс: конфиг из `NATS_*`, provide без тега. Непустое —
конфиг из `<NAME>_NATS_*`, provide под тегом `name:"<name>"`, поэтому инстансы не
коллизят в графе и приложение с двумя шинами (своя + внешняя) собирается штатно:

```go
natskit.Module(""),     // своя шина
natskit.Module("hub"),  // внешняя, HUB_NATS_*
fx.Provide(fx.Annotate(NewAlertPublisher, fx.ParamTags(`name:"hub"`))),
```

`Probe` модуль **не** provide'ит: тип `func(context.Context) error` коллизит с
health-probe'ами других китов (например `cachekit.Probe`), а класть в readiness
имеет смысл не каждый инстанс. Регистрация — отдельной опцией
`natskit.HealthProbe(name)`.

## Ключевые проектные решения (WHY)

- **Два интерфейса, не один.** `Bus` (Core) и `StreamBus` (JetStream) разнесены:
  у них разные гарантии (fire-and-forget vs at-least-once) и разная семантика
  ошибки обработчика (drop vs ретрай/DLQ). Один интерфейс скрыл бы эту разницу и
  провоцировал ошибочные ожидания о доставке.
- **`disabledStreamBus` вместо `nil`.** При `NATS_JETSTREAM_ENABLED=false`
  `StreamBus` собирается, но его методы возвращают `ErrJetStreamDisabled`. fx-граф
  не ломается, а неверное использование выключенного транспорта — явная ошибка в
  рантайме, не nil-panic.
- **Дедуп — на стороне стрима, по `Nats-Msg-Id`.** Кит не хранит состояние
  публикаций; effectively-once персист обеспечивает сам JetStream в окне
  `Duplicates`. Это согласовано с `outboxkit`, который ставит namespaced
  `Nats-Msg-Id` при публикации из outbox.
- **DLQ — Core-публикация, best-effort.** Dead-letter уходит обычным
  `conn.PublishMsg` (не в JetStream): DLQ — диагностический канал, не источник
  истины. Term всё равно останавливает ределивери; сбой DLQ-публикации
  логируется, но не блокирует Term.
- **Backpressure обязателен.** Без семафора всплеск входящих породил бы
  неограниченное число горутин-обработчиков и съел бы память. `MaxInFlight`
  (default 100) — потолок конкуренции; для JetStream он же `MaxAckPending`.
- **panic не роняет процесс.** Обработчик в отдельной горутине обёрнут recover:
  паника одного сообщения логируется со стеком и не валит весь подписчик. В Core
  сообщение при этом теряется; в JetStream — идёт по poison-пути (NAK→…→DLQ/Term).
- **`Config` не в `Module`.** Симметрично остальным китам: env-контракт собирает
  приложение, кит лишь его потребляет.
