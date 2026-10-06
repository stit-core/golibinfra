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
panic recovery в обработчиках, backpressure через семафор, handle подписки
(`Subscription`), graceful `Close` с ожиданием обработчиков, W3C
trace-propagation и метрики `nats_*`.

Доменное — вне кита: схемы сообщений, идемпотентность по бизнес-ключу,
retry-политика бизнес-уровня, выбор subject'ов и stream'ов. `natskit` отвечает
за доставку байтов и надёжность транспорта, не за смысл сообщений.

## Компоненты

| Файл | Роль |
|---|---|
| `bus.go` | `Bus`-интерфейс (`Publish`/`Subscribe`/`SubscribeQueue`), `natsBus` (Core NATS), `ErrTerminal`, дженерики `PublishJSON[T]`/`SubscribeJSON[T]`/`SubscribeQueueJSON[T]`, `decodeJSON`. |
| `publish.go` | `PublishOption` и `PublishSpec` — общие опции публикации обоих транспортов (`WithMsgID`, `WithHeader`, `WithMetricSubject`, `WithTraceParent`). |
| `jetstream.go` | `StreamBus`-интерфейс, `jsBus` (JetStream), `disabledStreamBus` (no-op при выключенном JS), `StreamChecker` (`CheckStreams` — стримы публикатора), `ensureStreams`/`reconcileStream`, deadline + `InProgress` обработчика (`keepInProgress`), poison-обработка (`failStream`/`terminalReason`/`deadLetter`), `StreamMsg`, `SubjectMatches`, дженерики `PublishStreamJSON[T]`/`SubscribeStreamJSON[T]`. |
| `subscription.go` | `Handler`, `Subscription` (`Unsubscribe`/`Drain`), внутренняя `subscription` и `inflightGroup` — учёт работающих обработчиков. |
| `conn.go` | `Conn` — соединение + реестр подписок; `NewConn` (аутентификация, handlers nats.go → logkit), `Healthy`, graceful `Close`/`CloseContext`, `redactURL`, ctx обработчиков. |
| `client.go` | `Client` — инстанс шины (соединение + `Bus` + `StreamBus`), `New`, `Probe` (`healthkit.Probe`). |
| `config.go` | `Config` / `BackpressureConfig` / `JetStreamConfig` — публичный env-контракт; `NewConfig`, `NewInstanceConfig` (ENV — ридером китов `internal/envconf`, префикс инстанса без fallback на беспрефиксные переменные), `Validate` и `ErrConfig`. |
| `context.go` | `SubjectFromContext` / `HeadersFromContext` — доступ к subject и header'ам входящего сообщения внутри обработчика. |
| `instrumentation.go` | `natsInstruments` (5 otel-инструментов через `internal/otelkit`), producer/consumer-спаны, `natsHeaderCarrier` (TextMapCarrier на `nats.Header` для trace-propagation). |
| `module.go` | fx `Module(name)` (`*Client` + `Bus` + `StreamBus`), `ProvideConfig(name)`, `ProvideProbe(name)`, `SubscribeOnStart` — подписка в `OnStart`, drain в `OnStop`. |

## Bus (Core NATS)

`natsBus` оборачивает `*nats.Conn`. Поток:

```
Publish(ctx, subject, payload []byte, opts...)
   │  startPublishSpan: producer-span + заголовки опций + inject trace-context
   ▼
conn.PublishMsg(msg) ──► nats_messages_published_total{status[,subject]}
                         nats_publish_duration_seconds[{subject}]

Subscribe / SubscribeQueue ──► Subscription (Unsubscribe / Drain)
   │  callback на каждое входящее сообщение
   ▼
sem.Acquire(1) ──► track ──► go handleOne(...)   (backpressure: не более MaxInFlight)
   │  inflight +1
   │  startConsumerSpan: extract trace-context → child-span; subject+headers в ctx
   │  ctx = deadline HandlerTimeout (NATS_HANDLER_TIMEOUT), отменяется при force-close
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

`ctx` обработчика несёт deadline `NATS_HANDLER_TIMEOUT` (30s по умолчанию) и
отменяется, когда `Close` исчерпал бюджет drain: зависший обработчик не держит
слот семафора до конца жизни процесса и получает сигнал свернуться.

`ctx` в `Subscribe`/`SubscribeQueue` ограничивает только регистрацию (отменённый
ctx → ошибка сразу). Время жизни подписки — `Subscription` и `Close` инстанса:
привязка к ctx ловила бы `OnStart`-хук fx, чей ctx отменяется сразу после старта.

## StreamBus (JetStream)

`jsBus` активен только при `NATS_JETSTREAM_ENABLED=true`; иначе `NewStreamBus` (формат
JetStream-полей он проверяет и у выключенного) возвращает `disabledStreamBus`, у которого все методы отдают
`ErrJetStreamDisabled` (no-op, не nil — приложение не падает при сборке графа,
а получает явную ошибку при попытке использовать выключенный транспорт).

### Декларация стримов

При старте `ensureStreams` проходит по `NATS_JS_STREAM_NAMES`. Режим задаёт
`NATS_JS_STREAM_MANAGED`:

- `true` — `reconcileStream`: отсутствующий стрим создаётся целиком (subjects
  `<name>.>`, file-storage, retention/`MaxAge`/`Duplicates` из конфига;
  `Duplicates` поджимается до `MaxAge`, если задан больше — требование
  JetStream). У существующего стрима сверяются только subjects: `<name>.>`
  доливается объединением, остальные поля (`Replicas`, `MaxBytes`, `MaxAge`,
  чужие subjects, описание) не трогаются. Гонка двух инстансов на создании
  (`ErrStreamNameAlreadyInUse`) уходит в ту же сверку subjects.
- `false` — кит только проверяет, что каждый стрим существует; иначе ошибка
  старта.

### Проверка стримов публикатора

`CheckStreams` (интерфейс `StreamChecker`) сначала берёт `AccountInfo`: сервер без JetStream
отвечает `ErrJetStreamNotEnabled` — это `ErrJetStreamDisabled`, а на публикацию он даёт ту же
`no response from stream`, что subject без стрима. Затем — `ListStreams` с фильтром subject:
сервер отдаёт стримы, чьи subjects пересекаются с фильтром, а накрыть его целиком должен
subject одного из них (`SubjectMatches` с фильтром в роли subject) — иначе `ErrNoStream` с
фильтром в тексте. У `disabledStreamBus` — `ErrJetStreamDisabled`. Это отдельный интерфейс, а не метод
`StreamBus`: фейки шины (`testkit/natstest`) стримов не знают, и их потребители проверку
пропускают.

### Публикация

- `PublishStream` — синхронно: `PublishMsg` + ждёт `PubAck` (в пределах ctx или
  дефолтного таймаута JetStream API).
- `PublishStreamBatch` — async: все сообщения уходят `PublishMsgAsync`, затем
  метод ждёт исход **каждого своего** future'а (`results[i] == nil` — персист
  подтверждён). Ожидание пер-future, а не по `PublishAsyncComplete()`: тот канал
  общий на весь `jetstream.JetStream`, и потерянный `PubAck` соседнего батча
  задерживал бы этот на весь `PublishAsyncTimeout`. Внешняя ошибка — сбой всего
  батча (JS выключен / отмена `ctx`). Порядок персиста best-effort: при частичном сбое
  неподтверждённые уходят на ретрай и могут получить более поздний sequence —
  дедуп по `MsgID` гарантирует отсутствие дублей. Потерянный `PubAck` (обрыв
  сети между `PUB` и ответом) закрывается `jetstream.WithPublishAsyncTimeout`
  (`NATS_JS_PUBLISH_ASYNC_TIMEOUT`): `results[i] = ErrAsyncPublishTimeout`, а не
  вечное ожидание — на это опирается `outboxkit`.

`WithMsgID(id)` (или `StreamMsg.MsgID`) ставит `Nats-Msg-Id`: в пределах окна
`Duplicates` повторная публикация с тем же id дедуплицируется стримом
(effectively-once персист при ретраях продьюсера).

### Consumer reliability

`SubscribeStream(stream, subject, durable, handler)` создаёт/обновляет durable
pull-консьюмера с `AckExplicitPolicy`, `FilterSubject=subject`,
`AckWait=NATS_JS_ACK_WAIT`, `MaxAckPending=NATS_JS_MAX_IN_FLIGHT` и запускает
`Consume`; ошибки pull-цикла (нет heartbeat, консьюмер удалён) логируются
(`natskit: jetstream consume error`). Обработка каждого сообщения
(`handleStreamOne`):

```
sem.Acquire(1) ──► track ──► go handleStreamOne(...)
   │ inflight +1
   │ ctx = consumer-span + deadline HandlerTimeout
   │ keepInProgress: каждые AckWait/2 → msg.InProgress(), пока handler работает
   ▼
invoke(handler): panic → recover (panicked=true), иначе вернуть ошибку handler'а
   │
   ├─ успех ───────────────► msg.Ack()          status=ok
   └─ ошибка/panic ────────► failStream(...)
                                │
           errors.Is(err, ErrTerminal) или NumDelivered ≥ MaxDeliver?
                ├─ да  → deadLetter (если DLQSubject задан)
                │        ├─ копия ушла → log error + msg.TermWithReason(cause)  status=dead
                │        └─ отказ DLQ  → log error + Nak                   status=dlq_error
                └─ нет → NakWithDelay(растущая задержка) | Nak()  status=handler_error|panic
```

- **Deadline + `InProgress`.** Сервер переотправляет сообщение, не дождавшись
  `Ack` за `AckWait`; без продления живой, но медленный обработчик получал бы
  конкурентный ределивери. `keepInProgress` сдвигает таймер сервера, пока
  обработчик работает, а `HandlerTimeout` (по умолчанию = `AckWait`) — deadline
  `ctx` обработчика: зависший обработчик получает отмену, возвращает ошибку и
  сообщение идёт по poison-пути. После deadline `InProgress` не шлётся —
  обработчик, игнорирующий `ctx`, получит ределивери, пока висит.
- **`ErrTerminal`** — обработчик заявляет, что ретрай бессмыслен (чужая схема,
  битый payload): кит идёт в DLQ/`Term` с первой попытки. Тем же маркером
  обёрнута ошибка декода `Subscribe*JSON`.
- **`MaxDeliver`** — потолок числа доставок. Ставится и на консьюмере
  (server-side backstop), и проверяется клиентом перед `Term`. `MaxDeliver == 0`
  — без лимита (бесконечный ределивери).
- **NAK-backoff** — при `NakBackoff > 0` неуспешное сообщение возвращается с
  задержкой (`NakWithDelay`), иначе мгновенно (`Nak`). Задержка растёт с номером
  доставки — `NakBackoff × 2^(n−1)` до `NakMaxBackoff`: короткий сбой зависимости
  переживается за секунды, долгий — с ретраем раз в потолок, без горячей петли.
  Бюджет дефолтов (`MaxDeliver=21`, `1s`, `1m`) — 15 минут ретраев
  ([configuration.md](configuration.md#бюджет-ретраев)).
- **Ошибку обработчика логирует кит.** `error`-запись `natskit: jetstream handler
  error` (Core — `natskit: handler error`) несёт `subject`, `trace_id` спана
  обработки и саму ошибку; обработчик её возвращает и не пишет в лог второй раз.
- **DLQ** — на терминальном отказе сообщение перед `Term` republish'ится в
  `DLQSubject` с заголовками `Natskit-Dlq-Origin`/`-Error`/`-Reason`/`-Stream`/
  `-Consumer`/`-Deliveries`/`-Instance`/`-Msg-Id` и остальными исходными
  header'ами (полный список — [configuration.md](configuration.md)).
  `Nats-Msg-Id` в копию не переносится (иначе стрим с окном дедупа выбросил бы
  копию как дубль), `Nats-Expected-*` не копируются (они адресованы исходному
  стриму). Публикация идёт через JetStream, если DLQ-subject накрыт стримом
  (решается один раз на старте, `StreamNameBySubject`), иначе Core-публикацией
  с `Flush`. Сам отказ — всегда `error`-запись `natskit: jetstream message dead`,
  поэтому и без `DLQSubject` причина не теряется.
- **Фильтр не должен накрывать DLQ-subject.** `SubscribeStream` проверяет это на
  регистрации (`SubjectMatches`) и возвращает `ErrConfig`: иначе DLQ-копия
  вернулась бы тому же обработчику и цикл «отказ → копия → отказ» рос бы в стриме
  без остановки.
- **Граница DLQ.** Копия появляется, только если последняя доставка провалилась в
  живом процессе. Умерший процесс и истёкший `AckWait` на последней доставке
  оставляют сообщение неподтверждённым в стриме: сервер больше не доставляет
  его, и клиенту нечего дед-леттерить.

`durable` — имя durable-консьюмера: переподключение продолжает с того же места,
а не с начала стрима. Несколько инстансов с одним `durable` делят нагрузку
(JetStream-аналог queue-группы).

## Subscription и graceful shutdown

Каждая подписка регистрируется в `Conn` и считает свои работающие обработчики
(`inflightGroup`; тот же счётчик есть у `Conn` целиком).

- `Subscription.Unsubscribe()` — снимает интерес сразу; уже полученные, но не
  начатые сообщения и доставки, которые транспорт отдаст после него, не
  обрабатываются (JetStream переотправит их после `AckWait`), работающие
  обработчики не ждутся.
- `Subscription.Drain(ctx)` — снимает интерес, даёт обработать буфер и ждёт
  завершения обработчиков в пределах ctx.
- `Conn.Close()` (= `Client.Close()`, `OnStop`-хук `Module`) с бюджетом
  `DrainTimeout`: drain всех подписок → ожидание всех обработчиков → `nc.Drain()`
  (flush публикаций) → ожидание `ClosedHandler`. Порядок важен: обработчики
  завершаются по живому соединению и успевают `Ack`/`Nak`. По истечении бюджета
  — предупреждение в лог и принудительный `nc.Close()`. После начала `Close`
  новые подписки получают `ErrClosed`.

Счётчик обработчиков — не `sync.WaitGroup`: транспорт может доставить сообщение
после истечения бюджета drain, а `WaitGroup` запрещает `Add` во время `Wait`.

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
обрабатывается в отдельной горутине, но не больше `MaxInFlight` одновременно.
При исчерпании семафора delivery-горутина подписки блокируется, и новые
сообщения копятся в клиентском буфере nats.go:

- Core: буфер конечен (pending limits nats.go); переполнение — `slow consumer`,
  сообщения теряются, клиент сообщает об этом через `ErrorHandler` → лог
  `natskit: async error` с числом `dropped`.
- JetStream: `MaxAckPending = MaxInFlight` — потолок на **консьюмер**, а семафор
  один на все подписки инстанса. С одной подпиской он почти не блокирует, с N
  консьюмерами блокировка штатна; на время callback'а `Consume` останавливает
  heartbeat-монитор, поэтому ложных `consume error` из-за ожидания на семафоре
  не бывает.

Семафор защищает сервис от перегрузки при всплеске трафика; конкуренция
обработчиков ограничена, остальное ждёт в буфере.

## Соединение

`NewConn` валидирует конфиг, собирает опции аутентификации (`NATS_CREDS_FILE`,
`NATS_NKEY_SEED_FILE`, `NATS_TLS_CA_FILE`) и вешает на `nats.Conn` handlers,
которые пишут в `logkit.Logger`: `ErrorHandler` (slow consumer, ошибки
протокола), `DisconnectErrHandler`, `ReconnectHandler`, `ClosedHandler`.
`DrainTimeout` пробрасывается и в nats.go (`nats.DrainTimeout`), чтобы его
внутренний drain укладывался в тот же бюджет.

Любой логируемый URL проходит `redactURL`: userinfo (пароль или токен из
`NATS_URL`) заменяется на `xxxxx`. `ClosedHandler` вне `Close` означает
исчерпание reconnect'ов — это `error`-запись с `LastError`, а `Healthy` с этого
момента отдаёт `ErrConnectionClosed`, отличимый от временного `RECONNECTING`.

## fx Module

`Module(name)` provide'ит `*Client`, `Bus` и `StreamBus` одного инстанса и вешает
`OnStop`-хук с `Client.CloseContext(ctx)` — graceful shutdown в пределах
`StopTimeout` приложения.

Пустое имя — дефолтный инстанс: `Config` без тега (`fx.Provide(natskit.NewConfig)`).
Непустое — `Config` под тегом `name:"<name>"` (`natskit.ProvideConfig(name)`
читает `<NAME>_NATS_*`), и под тем же тегом provide'ятся `*Client`, `Bus`,
`StreamBus`. Инстансы не коллизят в графе, приложение с двумя шинами собирается
штатно:

```go
fx.Provide(natskit.NewConfig), natskit.Module(""),        // своя шина
natskit.ProvideConfig("hub"), natskit.Module("hub"),      // внешняя, HUB_NATS_*
fx.Provide(fx.Annotate(NewAlertPublisher, fx.ParamTags(`name:"hub"`))),
```

Имя инстанса ограничено `[A-Za-z0-9_.-]`: из него собирается ENV-префикс, и имя
вроде `hub/v2` дало бы переменные, которые невозможно задать. `Module` и
`ProvideConfig` отвергают такое имя `ErrConfig` при сборке графа.

`Probe` модуль **не** provide'ит: в readiness имеет смысл класть не каждый
инстанс. Регистрация — `natskit.ProvideProbe(name)` (`""` — дефолтный инстанс): она кладёт
`healthkit.Probe` в группу `healthkit.probes`. Имя пробы — как у модуля инстанса: `natskit` или
`natskit:<name>`.

## Ключевые проектные решения (WHY)

- **Два интерфейса, не один.** `Bus` (Core) и `StreamBus` (JetStream) разнесены:
  у них разные гарантии (fire-and-forget vs at-least-once) и разная семантика
  ошибки обработчика (drop vs ретрай/DLQ). Один интерфейс скрыл бы эту разницу и
  провоцировал ошибочные ожидания о доставке.
- **`disabledStreamBus` вместо `nil`.** При `NATS_JETSTREAM_ENABLED=false`
  `StreamBus` собирается, но его методы возвращают `ErrJetStreamDisabled`. fx-граф
  не ломается, а неверное использование выключенного транспорта — явная ошибка в
  рантайме, не nil-panic.
- **Стримы: создать или долить subject, не переписывать.** Полный `UpdateStream`
  из конфига кита затирал бы ручной тюнинг оператора (`Replicas`, `MaxBytes`,
  доп. subjects). Кит владеет только своим subject `<name>.>`; всё остальное —
  зона оператора, а `NATS_JS_STREAM_MANAGED=false` отдаёт ему и это.
- **Бюджет ретраев — минуты, а не секунды.** Транзиентный сбой зависимости
  обработчика (failover Postgres, выкатка соседа) длится десятки секунд и
  минуты; постоянная секундная задержка при ограниченном `MaxDeliver` отдала бы
  такое сообщение в `Term` до восстановления. Растущая задержка с потолком и
  `MaxDeliver=21` держат его 15 минут, а `Term` получают только `ErrTerminal` и
  исчерпанный бюджет. Сообщение на ретрае держит слот `MaxAckPending`: при сбое
  зависимости это backpressure, отказ, который ретрай не исправит, — `ErrTerminal`.
  `MaxAckPending` выше `MaxInFlight` слот бы не освободил: лишние сообщения ждали бы
  семафор под тикающим `AckWait` и уходили бы на ределивери.
- **`HandlerTimeout` отдельно от `AckWait`.** `AckWait` — про смерть процесса
  (за сколько сообщение уйдёт другому инстансу), бюджет обработки — про зависший
  код. Смешивать их значило бы либо ставить `AckWait` в минуты (медленный
  failover), либо резать долгие обработчики.
- **Дедуп — на стороне стрима, по `Nats-Msg-Id`.** Кит не хранит состояние
  публикаций; effectively-once персист обеспечивает сам JetStream в окне
  `Duplicates`. Это согласовано с `outboxkit`, который ставит namespaced
  `Nats-Msg-Id` при публикации из outbox.
- **DLQ — по месту хранения subject'а, без `Nats-Msg-Id`.** Если DLQ-subject
  накрыт стримом, копия публикуется через JetStream и подтверждается `PubAck`;
  иначе — Core-публикацией с `Flush` (JetStream ждал бы `PubAck`, которого некому
  прислать, и задержал бы `Term` до ределивери). Исходный id сохраняется в
  `Natskit-Dlq-Msg-Id`.
- **`Term` — только после успешной копии.** Провал DLQ-публикации оставляет
  сообщение в стриме (`Nak`, `status=dlq_error`): недоступный DLQ иначе стирал
  бы данные молча. Цена — ретраи продолжаются, пока DLQ не оживёт; при
  `MaxDeliver > 0` их обрывает server-side backstop.
- **Backpressure обязателен.** Без семафора всплеск входящих породил бы
  неограниченное число горутин-обработчиков и съел бы память. `MaxInFlight`
  (default 100) — потолок конкуренции; для JetStream он же `MaxAckPending`.
- **panic не роняет процесс.** Обработчик в отдельной горутине обёрнут recover:
  паника одного сообщения логируется со стеком и не валит весь подписчик. В Core
  сообщение при этом теряется; в JetStream — идёт по poison-пути (NAK→…→DLQ/Term).
- **Drain с бюджетом, не бесконечный.** Shutdown обязан завершиться: оркестратор
  всё равно убьёт процесс по своему таймауту, и лучше закрыть соединение
  осознанно с предупреждением в логе, чем получить SIGKILL посреди обработки.
- **`Config` не в `Module`.** Симметрично остальным китам: env-контракт собирает
  приложение (`fx.Provide(natskit.NewConfig)` / `natskit.ProvideConfig(name)`),
  кит лишь его потребляет — конфиг можно подменить своим конструктором, не
  прибегая к `fx.Decorate`.
- **Переменные инстанса без запасного имени.** `NewInstanceConfig(name)` читает
  только `<NAME>_NATS_*`: возьми он при незаданной префиксной переменной
  одноимённую беспрефиксную, именованный инстанс молча унаследовал бы URL и стримы
  дефолтного — то есть ушёл бы публиковать в чужую шину.
- **`Validate` на старте.** URL без хоста или с чужой схемой, отрицательный таймаут
  и `NATS_JS_ACK_WAIT=1ns` (keepalive ломается на первом сообщении) отвергаются
  целиком в `NewConfig`/`NewInstanceConfig`/`New`: сервис падает на старте с
  `ErrConfig`, а не на первой публикации.
