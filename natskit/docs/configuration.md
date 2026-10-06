# Конфигурация natskit

`natskit.Config` (с вложенными `BackpressureConfig` и `JetStreamConfig`) —
публичный env-контракт. Загружается `natskit.NewConfig()` (голые `NATS_*`) либо
`natskit.NewConfigPrefixed(name)` (`<NAME>_NATS_*` — конфиг именованного
инстанса); оба валидируют результат и возвращают `ErrConfig` на невалидном
значении. Конфиг в fx-граф кладёт приложение (`fx.Provide(natskit.NewConfig)` /
`natskit.ProvideConfig(name)`) — `Module` его не читает. Поле `Config.Instance`
из ENV не читается: его проставляют `New`/`NewConfigPrefixed`, и оно едет
атрибутом `instance` в метрики, спаны и логи.

> **Public API.** Имена полей и их `envconfig`/`default`-теги — часть стабильного
> контракта. Менять их = breaking change: выпускается minor-релизом, помечается
> `!` в Conventional Commit и строкой миграции в аннотации тега (см. [корневой
> README § Версионирование](../../README.md)).

**Заданная, но пустая переменная равна незаданной** — берётся `default`-тег.
`NATS_MAX_RECONNECTS=` в `.env`, Helm с `value: ""` и compose с незаданной
подстановкой дают именно такую переменную; без этого правила она молча обнуляла
бы безопасный дефолт (ни одной попытки reconnect'а, ретрай без backoff'а). Поле
без `default`-тега остаётся нулевым — так же, как если бы переменной не было.
Непустое, но неразбираемое значение (`NATS_JS_ACK_WAIT=30` без единицы) —
`ErrConfig` на старте.

## Соединение и Core NATS

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `NATS_URL` | `URL` | `string` | `nats://localhost:4222` | Адрес NATS-сервера. «Шов» под split-топологию — разное значение = разный хост. |
| `NATS_CONNECT_TIMEOUT` | `ConnectTimeout` | `time.Duration` | `5s` | Таймаут первичного подключения. |
| `NATS_RECONNECT_WAIT` | `ReconnectWait` | `time.Duration` | `2s` | Пауза между попытками переподключения. |
| `NATS_MAX_RECONNECTS` | `MaxReconnects` | `int` | `-1` | Максимум попыток переподключения после разрыва. `-1` — бесконечно (дефолт): NATS-maintenance любой длины переживается без потери шины. `≥ 0` — потолок попыток, после которого соединение закрывается навсегда (см. [Исчерпание reconnect'ов](#исчерпание-reconnectов)). Меньше `-1` — `ErrConfig`. |
| `NATS_DRAIN_TIMEOUT` | `DrainTimeout` | `time.Duration` | `10s` | Общий бюджет `Close`: drain подписок + ожидание работающих обработчиков + flush публикаций. По истечении ctx обработчиков отменяется и соединение закрывается принудительно. `0` → `10s`, отрицательное — `ErrConfig`. |
| `NATS_HANDLER_TIMEOUT` | `HandlerTimeout` | `time.Duration` | `30s` | Бюджет одного Core-обработчика: deadline `ctx`, переданного в handler. `0` — без ограничения (зависший обработчик держит слот `MaxInFlight` до конца процесса), отрицательное — `ErrConfig`. Для JetStream действует `NATS_JS_HANDLER_TIMEOUT`. |

## Аутентификация

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `NATS_CREDS_FILE` | `CredsFile` | `string` | `""` | Путь к `.creds`-файлу (NATS JWT + nkey seed, `nats.UserCredentials`). |
| `NATS_NKEY_SEED_FILE` | `NKeySeedFile` | `string` | `""` | Путь к файлу nkey-seed (`nats.NkeyOptionFromSeed`). |
| `NATS_TLS_CA_FILE` | `TLSCAFile` | `string` | `""` | Корневой сертификат для TLS-подключения (`nats.RootCAs`). |

Заданный путь проверяется на старте (`Validate`: файла нет — `ErrConfig`).
Простые креды (`user:password`, токен) живут в `NATS_URL`; в логи и в тексты
ошибок userinfo попадает как `nats://xxxxx@host:4222`.

## Backpressure (`Config.Backpressure`)

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `NATS_MAX_IN_FLIGHT` | `Backpressure.MaxInFlight` | `int` | `100` | Потолок одновременно обрабатываемых Core-сообщений (вес семафора `natsBus`). `0` → `100`, отрицательное — `ErrConfig`. |

## JetStream (`Config.JetStream`)

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `NATS_JETSTREAM_ENABLED` | `JetStream.Enabled` | `bool` | `false` | Включает `StreamBus`. При `false` — `disabledStreamBus` (все методы → `ErrJetStreamDisabled`). |
| `NATS_JS_STREAM_NAMES` | `JetStream.StreamNames` | `[]string` | — (пусто) | Стримы кита (через запятую). Subjects стрима — `<name>.>`. Пусто — кит стримы не декларирует и не проверяет. Имя с точкой, пробелом или wildcard — `ErrConfig`. |
| `NATS_JS_STREAM_MANAGED` | `JetStream.StreamManaged` | `bool` | `true` | `true` — кит создаёт отсутствующие стримы и доливает свой subject в существующие (см. [Декларация стримов](#декларация-стримов)). `false` — кит стримы не трогает, только проверяет наличие каждого из `StreamNames` (отсутствие — ошибка старта). В структурном литерале `Config{}` поле надо выставлять явно. |
| `NATS_JS_RETENTION` | `JetStream.Retention` | `string` | `limits` | Политика удержания создаваемого стрима: `limits` / `interest` / `workqueue` (он же `work_queue`). Иное значение — `ErrConfig`. |
| `NATS_JS_MAX_AGE` | `JetStream.MaxAge` | `time.Duration` | `168h` | Максимальный возраст сообщения в создаваемом стриме. `0` → `168h` (7 дней), отрицательное — `ErrConfig`. |
| `NATS_JS_DUPLICATES` | `JetStream.Duplicates` | `time.Duration` | `2m` | Окно дедупа по `Nats-Msg-Id` создаваемого стрима. `0` → `2m`, отрицательное — `ErrConfig`. Поджимается до `MaxAge`, если задано больше (требование JetStream: окно дедупа ≤ срок хранения). |
| `NATS_JS_MAX_IN_FLIGHT` | `JetStream.MaxInFlight` | `int` | `100` | Потолок одновременно обрабатываемых stream-сообщений (вес семафора `jsBus`) и `MaxAckPending` консьюмера. `0` → `100`, отрицательное — `ErrConfig`. |
| `NATS_JS_ACK_WAIT` | `JetStream.AckWait` | `time.Duration` | `30s` | Сколько сервер ждёт `Ack`, прежде чем переотправить сообщение. Пока обработчик работает, кит каждые `AckWait/2` шлёт `InProgress`, продлевая ожидание — таймер срабатывает только если процесс умер. `0` → `30s`; значение меньше `1s` — `ErrConfig` (за такое окно `InProgress` не успевает). |
| `NATS_JS_HANDLER_TIMEOUT` | `JetStream.HandlerTimeout` | `time.Duration` | `0` | Бюджет одной попытки обработчика: deadline `ctx`, переданного в handler. `0` — равен `AckWait`, отрицательное — `ErrConfig`. По истечении обработчик, уважающий `ctx`, возвращает ошибку → `Nak` → ретрай. |
| `NATS_JS_PUBLISH_ASYNC_TIMEOUT` | `JetStream.PublishAsyncTimeout` | `time.Duration` | `5s` | Сколько `PublishStreamBatch` ждёт `PubAck` одного сообщения. Потерянный ack завершается `jetstream.ErrAsyncPublishTimeout` в `results[i]`, а не висит вечно. `0` → `5s`, отрицательное — `ErrConfig`. |
| `NATS_JS_MAX_DELIVER` | `JetStream.MaxDeliver` | `int` | `21` | Потолок числа доставок одного сообщения. `0` — без лимита (бесконечный ределивери), отрицательное — `ErrConfig`. `> 0` — на исчерпании DLQ/Term. С дефолтными задержками 21 доставка — 15 минут ретраев (см. [Бюджет ретраев](#бюджет-ретраев)). |
| `NATS_JS_NAK_BACKOFF` | `JetStream.NakBackoff` | `time.Duration` | `1s` | Задержка ределивери после первой доставки; после каждой следующей она удваивается до `NATS_JS_NAK_MAX_BACKOFF` (`NakWithDelay`). `0` — мгновенный `Nak` без роста (горячая петля на устойчиво падающем сообщении), отрицательное — `ErrConfig`. |
| `NATS_JS_NAK_MAX_BACKOFF` | `JetStream.NakMaxBackoff` | `time.Duration` | `1m` | Потолок растущей задержки NAK. `0` → `1m`, отрицательное — `ErrConfig`; потолок меньше `NATS_JS_NAK_BACKOFF` — задержка постоянная, равная `NATS_JS_NAK_BACKOFF`. |
| `NATS_JS_DLQ_SUBJECT` | `JetStream.DLQSubject` | `string` | `""` | Subject для dead-letter. Пусто — на терминальном отказе только `Term` (сообщение при этом логируется). Задан — перед `Term` сообщение republish'ится в DLQ. Значение с пробелом, пустым токеном или wildcard — `ErrConfig`. |

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

Четыре параметра определяют судьбу неуспешного stream-сообщения. Работают вместе:

| Цель | `NATS_JS_MAX_DELIVER` | `NATS_JS_NAK_BACKOFF` / `NATS_JS_NAK_MAX_BACKOFF` | `NATS_JS_DLQ_SUBJECT` |
|---|---|---|---|
| Ретраи 15 минут + dead-letter на разбор (дефолт кита + subject) | `21` | `1s` / `1m` | напр. `dlq.orders` |
| Ограниченный ретрай, разбор по логам | `> 0` | `> 0` | — |
| Бесконечный ретрай (доставка не теряется, риск «застрять») | `0` | `> 0` | — |

Сообщение больше не ретраится в двух случаях: обработчик вернул ошибку,
обёрнутую `natskit.ErrTerminal` (в т.ч. ошибку декода JSON у `Subscribe*JSON` —
битый payload не станет валидным при ределивери), либо `NumDelivered ≥
MaxDeliver`. Тогда кит публикует копию в `DLQSubject` (если он задан), пишет
`error`-запись `natskit: jetstream message dead` (с subject, `stream_seq` —
sequence сообщения в стриме, числом доставок, причиной последнего отказа) и
делает `TermWithReason`. До этого — `NakWithDelay` с растущей задержкой (или
`Nak` при `NATS_JS_NAK_BACKOFF=0`).

Заголовки DLQ-копии:

| Заголовок | Значение |
|---|---|
| `Natskit-Dlq-Origin` | Исходный subject сообщения. |
| `Natskit-Dlq-Error` | Текст ошибки последней попытки (без переводов строк). |
| `Natskit-Dlq-Reason` | `terminal` (`ErrTerminal`) или `max-deliver`. |
| `Natskit-Dlq-Stream` / `Natskit-Dlq-Consumer` | Стрим и durable-консьюмер, где сообщение умерло. |
| `Natskit-Dlq-Deliveries` | Сколько раз оно доставлялось. |
| `Natskit-Dlq-Instance` | Имя инстанса шины. |
| `Natskit-Dlq-Msg-Id` | Исходный `Nats-Msg-Id`, если был. |

Остальные заголовки оригинала копируются, кроме `Nats-Msg-Id` (переезжает в
`Natskit-Dlq-Msg-Id`, иначе стрим с окном дедупа счёл бы копию дублем и молча
выбросил) и `Nats-Expected-*` (они адресованы исходному стриму и отвергли бы
копию).

DLQ-subject может лежать внутри стрима: кит определяет это на старте
(`StreamNameBySubject`) и публикует копию через JetStream, получая `PubAck`;
иначе — Core-публикацией с `Flush` (без него отказ соединения остался бы
незамеченным).

**`Term` делается только после успешной DLQ-копии.** Провал публикации — это
`error`-запись `natskit: jetstream dlq publish failed, message kept for
redelivery`, `Nak` и метрика со `status=dlq_error`: оригинал остаётся в стриме и
уходит на ределивери, иначе недоступный DLQ стирал бы сообщение и из стрима, и
из DLQ. Цена — попытки продолжаются, пока DLQ не оживёт: при `MaxDeliver > 0`
их ограничивает сервер (после `MaxDeliver` доставок сообщение остаётся в стриме
до `MaxAge`), при `MaxDeliver=0` цикл `Nak` → обработчик → отказ DLQ идёт
бесконечно, раз в `NATS_JS_NAK_MAX_BACKOFF`. Серия
`nats_messages_consumed_total{status="dlq_error"}` — повод для алерта.

**Фильтр консьюмера не должен накрывать DLQ-subject.** `SubscribeStream` с таким
фильтром (`ORDERS.>` при `NATS_JS_DLQ_SUBJECT=ORDERS.dlq`) возвращает
`ErrConfig`: DLQ-копия пришла бы тому же обработчику, снова упала и породила
следующую копию. Читает DLQ отдельный консьюмер — со своим конфигом, в котором
`NATS_JS_DLQ_SUBJECT` не задан.

Ограничение JetStream: DLQ-копия появляется, только если **последняя** доставка
провалилась в живом процессе. Если процесс умер и `AckWait` истёк на
`MaxDeliver`-й доставке, сервер больше не доставляет сообщение, клиент про него
не узнаёт, DLQ не будет — сообщение остаётся в стриме неподтверждённым до
`MaxAge`.

### Бюджет ретраев

Задержка ределивери после n-й доставки — `NATS_JS_NAK_BACKOFF × 2^(n−1)`, но не
больше `NATS_JS_NAK_MAX_BACKOFF`. При дефолтах (`1s`, `1m`, 21 доставка) попытки
идут через 1, 2, 4, 8, 16, 32 секунды, затем раз в минуту: последняя доставка —
через 15 минут после первой (плюс время самих попыток). Сообщение переживает сбой
зависимости обработчика такой длины — failover Postgres, выкатку соседа, всплеск
503 — и обрабатывается в первую попытку после восстановления, не позже чем через
минуту. Бюджет меняет `NATS_JS_MAX_DELIVER`: при этих задержках каждая доставка
сверх седьмой добавляет к нему минуту.

Мёртвое сообщение остаётся в стриме с retention `limits` до его `MaxAge`: его
достают по `stream_seq` из `error`-записи и публикуют заново.

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

## Исчерпание reconnect'ов

При `NATS_MAX_RECONNECTS ≥ 0` клиент после `MaxReconnects × ReconnectWait`
закрывает соединение окончательно: подписки мертвы, `Publish` возвращает ошибку,
переподключения не будет. Кит пишет об этом `error`-запись `natskit: connection
closed, reconnects exhausted` с причиной (`LastError`), а `Client.Healthy()` и
health-проба отдают `natskit.ErrConnectionClosed` — состояние отличимо от
временного `RECONNECTING`. Дефолтные `-1` (бесконечные попытки) оставляют шину
живой и после длительной недоступности сервера.

## Graceful shutdown

`Client.Close()` в пределах `DrainTimeout`: дренит подписки (снимает интерес,
дообрабатывает уже полученные сообщения), ждёт завершения работающих
обработчиков, дренит публикации и закрывает соединение. Обработчики успевают
`Ack`/`Nak` по живому соединению. По истечении бюджета пишется предупреждение
(`natskit: drain timeout …`), `ctx` работающих обработчиков отменяется и
соединение закрывается принудительно; незавершённые JetStream-сообщения сервер
переотправит после `AckWait`.

`OnStop`-хук `Module` вызывает `Client.CloseContext(ctx)`: бюджет —
`min(DrainTimeout, дедлайн ctx)`, поэтому shutdown укладывается в `StopTimeout`
приложения, даже если `NATS_DRAIN_TIMEOUT` больше.

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

## Загрузка конфига

`natskit.NewConfig()` / `natskit.NewConfigPrefixed(name)` читают переменные выше
из окружения и валидируют результат. В fx-графе их регистрирует приложение:

```go
fx.Provide(natskit.NewConfig),  // дефолтный инстанс: NATS_*
natskit.ProvideConfig("hub"),   // именованный: HUB_NATS_* под тегом name:"hub"
```

Кит читает ENV своим загрузчиком, а не `envconfig.Process(prefix, &cfg)`:
последний при незаданной префиксной переменной подставляет значение одноимённой
беспрефиксной, и именованный инстанс молча унаследовал бы URL и стримы
дефолтного.
