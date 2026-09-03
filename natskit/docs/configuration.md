# Конфигурация natskit

`natskit.Config` (с вложенными `BackpressureConfig` и `JetStreamConfig`) —
публичный env-контракт. Загружается `natskit.NewConfig()` (голые `NATS_*`) либо
`natskit.NewConfigPrefixed(prefix)` (`<PREFIX>_NATS_*` — конфиг именованного
инстанса). Поле `Config.Instance` из ENV не читается: его проставляют
`New`/`Module`, и оно едет атрибутом `instance` в метрики, спаны и логи.

> **Public API.** Имена полей и их `envconfig`/`default`-теги — часть стабильного
> контракта. Менять их = breaking change, идёт мажорным апгрейдом (см.
> [корневой README § Стабильность API](../../README.md)).

## Соединение и Core NATS

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `NATS_URL` | `URL` | `string` | `nats://localhost:4222` | Адрес NATS-сервера. «Шов» под split-топологию — разное значение = разный хост. |
| `NATS_CONNECT_TIMEOUT` | `ConnectTimeout` | `time.Duration` | `5s` | Таймаут первичного подключения. |
| `NATS_RECONNECT_WAIT` | `ReconnectWait` | `time.Duration` | `2s` | Пауза между попытками переподключения. |
| `NATS_MAX_RECONNECTS` | `MaxReconnects` | `int` | `60` | Максимум попыток переподключения после разрыва. |
| `NATS_DRAIN_TIMEOUT` | `DrainTimeout` | `time.Duration` | `10s` | Общий бюджет `Close`: drain подписок + ожидание работающих обработчиков + flush публикаций. По истечении соединение закрывается принудительно. `≤ 0` → `10s`. |

## Backpressure (`Config.Backpressure`)

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `NATS_MAX_IN_FLIGHT` | `Backpressure.MaxInFlight` | `int` | `100` | Потолок одновременно обрабатываемых Core-сообщений (вес семафора `natsBus`). `≤ 0` → `100`. |

## JetStream (`Config.JetStream`)

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `NATS_JETSTREAM_ENABLED` | `JetStream.Enabled` | `bool` | `false` | Включает `StreamBus`. При `false` — `disabledStreamBus` (все методы → `ErrJetStreamDisabled`). |
| `NATS_JS_STREAM_NAMES` | `JetStream.StreamNames` | `[]string` | `` (пусто) | Стримы кита. Subjects стрима — `<name>.>`. Пусто — кит стримы не декларирует и не проверяет. |
| `NATS_JS_STREAM_MANAGED` | `JetStream.StreamManaged` | `bool` | `true` | `true` — кит создаёт отсутствующие стримы и доливает свой subject в существующие (см. [Декларация стримов](#декларация-стримов)). `false` — кит стримы не трогает, только проверяет наличие каждого из `StreamNames` (отсутствие — ошибка старта). В структурном литерале `Config{}` поле надо выставлять явно. |
| `NATS_JS_RETENTION` | `JetStream.Retention` | `string` | `limits` | Политика удержания создаваемого стрима: `limits` / `interest` / `workqueue` (он же `work_queue`). Иное значение — ошибка при старте. |
| `NATS_JS_MAX_AGE` | `JetStream.MaxAge` | `time.Duration` | `168h` | Максимальный возраст сообщения в создаваемом стриме. `≤ 0` → `168h` (7 дней). |
| `NATS_JS_DUPLICATES` | `JetStream.Duplicates` | `time.Duration` | `2m` | Окно дедупа по `Nats-Msg-Id` создаваемого стрима. `≤ 0` → `2m`. Поджимается до `MaxAge`, если задано больше (требование JetStream: окно дедупа ≤ срок хранения). |
| `NATS_JS_MAX_IN_FLIGHT` | `JetStream.MaxInFlight` | `int` | `100` | Потолок одновременно обрабатываемых stream-сообщений (вес семафора `jsBus`) и `MaxAckPending` консьюмера. `≤ 0` → `100`. |
| `NATS_JS_ACK_WAIT` | `JetStream.AckWait` | `time.Duration` | `30s` | Сколько сервер ждёт `Ack`, прежде чем переотправить сообщение. Пока обработчик работает, кит каждые `AckWait/2` шлёт `InProgress`, продлевая ожидание — таймер срабатывает только если процесс умер. `≤ 0` → `30s`. |
| `NATS_JS_HANDLER_TIMEOUT` | `JetStream.HandlerTimeout` | `time.Duration` | `0` | Бюджет одной попытки обработчика: deadline `ctx`, переданного в handler. `0` (и `≤ 0`) — равен `AckWait`. По истечении обработчик, уважающий `ctx`, возвращает ошибку → `Nak` → ретрай. |
| `NATS_JS_PUBLISH_ASYNC_TIMEOUT` | `JetStream.PublishAsyncTimeout` | `time.Duration` | `5s` | Сколько `PublishStreamBatch` ждёт `PubAck` одного сообщения. Потерянный ack завершается `jetstream.ErrAsyncPublishTimeout` в `results[i]`, а не висит вечно. `≤ 0` → `5s`. |
| `NATS_JS_MAX_DELIVER` | `JetStream.MaxDeliver` | `int` | `0` | Потолок числа доставок одного сообщения. `≤ 0` — без лимита (бесконечный ределивери). `> 0` — на исчерпании DLQ/Term. |
| `NATS_JS_NAK_BACKOFF` | `JetStream.NakBackoff` | `time.Duration` | `0` | Задержка перед повторной доставкой при NAK. `0` — мгновенный `Nak`; `> 0` — `NakWithDelay`. |
| `NATS_JS_DLQ_SUBJECT` | `JetStream.DLQSubject` | `string` | `` (пусто) | Subject для dead-letter. Пусто — на исчерпании попыток только `Term` (тихий drop). Задан — перед `Term` сообщение republish'ится в DLQ (best-effort). |

## Декларация стримов

При `StreamManaged=true` кит на старте проходит по `StreamNames`:

- стрима нет — создаёт его целиком: subjects `<name>.>`, file-storage,
  `Retention`/`MaxAge`/`Duplicates` из конфига;
- стрим есть — сверяет только subjects: если `<name>.>` среди них нет, доливает
  его (объединение с существующими). `Replicas`, `MaxBytes`, `MaxAge`,
  `Duplicates`, описание, чужие subjects и прочие настройки оператора остаются как
  есть. Конфликт (например, subjects, пересекающиеся с `<name>.>`) — ошибка
  старта: разрешается либо руками в стриме, либо переводом кита в
  `StreamManaged=false`.

При `StreamManaged=false` кит не пишет в конфигурацию стримов вообще, но
требует, чтобы каждый стрим из `StreamNames` существовал: так сервис падает на
старте, а не на первой публикации. Режим для стримов, которые ведёт платформа
или соседняя команда.

## Тюнинг poison-обработки

Три параметра определяют судьбу неуспешного stream-сообщения. Работают вместе:

| Цель | `NATS_JS_MAX_DELIVER` | `NATS_JS_NAK_BACKOFF` | `NATS_JS_DLQ_SUBJECT` |
|---|---|---|---|
| Бесконечный ретрай (доставка не теряется, риск «застрять») | `0` | по вкусу | — |
| Ограниченный ретрай + тихий drop | `> 0` (напр. `5`) | напр. `5s` | — |
| Ограниченный ретрай + dead-letter на разбор | `> 0` | напр. `5s` | напр. `dlq.orders` |

Логика на исчерпании попыток: `NumDelivered ≥ MaxDeliver` → (опц. publish в
`DLQSubject` с заголовками `Natskit-Dlq-Origin`/`Natskit-Dlq-Error`/
`Natskit-Dlq-Msg-Id`) → `Term` (ределивери прекращается). До исчерпания —
`NakWithDelay(NakBackoff)` или `Nak`.

DLQ-subject может лежать внутри стрима с окном дедупа: `Nats-Msg-Id` оригинала в
копию не переносится (он переезжает в `Natskit-Dlq-Msg-Id`), иначе стрим счёл бы
копию дублем и молча выбросил.

## AckWait и HandlerTimeout

`AckWait` — таймер сервера: не получив `Ack` за это время, он переотправляет
сообщение. Пока обработчик работает, кит каждые `AckWait/2` шлёт `InProgress`,
и таймер сдвигается — живой, но медленный обработчик не получает конкурентный
ределивери. Таймер срабатывает по назначению, когда процесс умер или отрезан от
сети: сообщение уходит другому инстансу не позже чем через `AckWait`.

`HandlerTimeout` — бюджет одной попытки: deadline `ctx` обработчика. Обработчик
обязан уважать `ctx` (передавать в БД/HTTP-вызовы, проверять `ctx.Done()`):
по истечении он возвращает ошибку, кит делает `Nak`, и сообщение идёт на ретрай
(или в DLQ по `MaxDeliver`). Обработчик, игнорирующий `ctx`, после deadline
теряет `InProgress` и получит ределивери, пока висит.

Практика: `AckWait` = за сколько сообщение должно уйти другому инстансу при
смерти процесса (десятки секунд), `HandlerTimeout` = максимальная честная
длительность обработки (минуты для тяжёлых задач). При `HandlerTimeout=0` оба
равны `AckWait`.

## Backpressure-тюнинг

`MaxInFlight` — компромисс между пропускной способностью и потреблением памяти:
выше → больше параллельных обработчиков (выше throughput при I/O-bound
обработке), но больше одновременно живущих горутин и незакрытых ресурсов.
Для JetStream это же значение становится `MaxAckPending` консьюмера — потолком
числа неподтверждённых сообщений, которые сервер доставит до того, как начнёт
ждать ack'и. Слишком низкий `MaxAckPending` ограничит пропускную способность
даже при свободном CPU.

При исчерпании `MaxInFlight` delivery-горутина подписки блокируется на
семафоре: новые сообщения копятся в буфере клиента. Для Core NATS переполнение
буфера (`slow consumer`) — потеря сообщений, о которой сообщает
`ErrorHandler` в логах (`natskit: async error`); JetStream дальше
`MaxAckPending` не отправляет.

## Graceful shutdown

`Client.Close()` (его вызывает `OnStop`-хук `Module`) в пределах `DrainTimeout`:
дренит подписки (снимает интерес, дообрабатывает уже полученные сообщения),
ждёт завершения работающих обработчиков, дренит публикации и закрывает
соединение. Обработчики успевают `Ack`/`Nak` по живому соединению. По истечении
бюджета пишется предупреждение (`natskit: drain timeout …`) и соединение
закрывается принудительно; незавершённые JetStream-сообщения сервер
переотправит после `AckWait`.

## Несколько инстансов

`natskit.Module(name)` с непустым именем читает те же переменные под префиксом
имени в верхнем регистре: `<NAME>_NATS_URL`, `<NAME>_NATS_JETSTREAM_ENABLED`,
`<NAME>_NATS_JS_STREAM_NAMES` и т.д. (дефис, точка и пробел в имени становятся
`_`). Пример: `Module("hub")` → `NAME=HUB`.

Наборы инстансов изолированы: незаданная префиксная переменная берёт значение из
колонки Default, а **не** из одноимённой голой `NATS_*`. Инстанс, который только
публикует в стрим соседней команды, поэтому оставляет
`<NAME>_NATS_JS_STREAM_NAMES` незаданным — и не пытается создать у соседей свои
стримы.

## Прямая загрузка

`natskit.NewConfig()` / `natskit.NewConfigPrefixed(prefix)` читают переменные
выше из окружения. Через fx конфиг provide'ит сам `Module(name)` — приложению
достаточно подключить модуль.
