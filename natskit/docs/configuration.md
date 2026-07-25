# Конфигурация natskit

`natskit.Config` (с вложенными `BackpressureConfig` и `JetStreamConfig`) —
публичный env-контракт. Загружается `natskit.NewConfig()` (голые `NATS_*`) либо
`natskit.NewConfigPrefixed(prefix)` (`<PREFIX>_NATS_*` — конфиг именованного
инстанса). Поле `Config.Instance` из ENV не читается: его проставляют
`New`/`Module`, и оно едет атрибутом `instance` в метрики и спаны.

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

## Backpressure (`Config.Backpressure`)

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `NATS_MAX_IN_FLIGHT` | `Backpressure.MaxInFlight` | `int` | `100` | Потолок одновременно обрабатываемых Core-сообщений (вес семафора `natsBus`). `≤ 0` подменяется на `100`. |

## JetStream (`Config.JetStream`)

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `NATS_JETSTREAM_ENABLED` | `JetStream.Enabled` | `bool` | `false` | Включает `StreamBus`. При `false` — `disabledStreamBus` (все методы → `ErrJetStreamDisabled`). |
| `NATS_JS_STREAM_NAMES` | `JetStream.StreamNames` | `[]string` | `` (пусто) | Стримы, создаваемые/обновляемые при старте. Subjects стрима — `<name>.>`. Пусто — стримы не декларируются (предполагается, что их завёл кто-то ещё). |
| `NATS_JS_RETENTION` | `JetStream.Retention` | `string` | `limits` | Политика удержания: `limits` / `interest` / `workqueue` (он же `work_queue`). Иное значение — ошибка при старте. |
| `NATS_JS_MAX_AGE` | `JetStream.MaxAge` | `time.Duration` | `168h` | Максимальный возраст сообщения в стриме. `≤ 0` подменяется на `168h` (7 дней). |
| `NATS_JS_DUPLICATES` | `JetStream.Duplicates` | `time.Duration` | `2m` | Окно дедупа по `Nats-Msg-Id`. `≤ 0` → `2m`. Поджимается до `MaxAge`, если задано больше (требование JetStream: окно дедупа ≤ срок хранения). |
| `NATS_JS_MAX_IN_FLIGHT` | `JetStream.MaxInFlight` | `int` | `100` | Потолок одновременно обрабатываемых stream-сообщений (вес семафора `jsBus`) и `MaxAckPending` консьюмера. `≤ 0` → `100`. |
| `NATS_JS_ACK_WAIT` | `JetStream.AckWait` | `time.Duration` | `30s` | Сколько JetStream ждёт `Ack`, прежде чем переотправить сообщение. `≤ 0` → `30s`. |
| `NATS_JS_MAX_DELIVER` | `JetStream.MaxDeliver` | `int` | `0` | Потолок числа доставок одного сообщения. `≤ 0` — без лимита (бесконечный ределивери). `> 0` — на исчерпании DLQ/Term. |
| `NATS_JS_NAK_BACKOFF` | `JetStream.NakBackoff` | `time.Duration` | `0` | Задержка перед повторной доставкой при NAK. `0` — мгновенный `Nak`; `> 0` — `NakWithDelay`. |
| `NATS_JS_DLQ_SUBJECT` | `JetStream.DLQSubject` | `string` | `` (пусто) | Subject для dead-letter. Пусто — на исчерпании попыток только `Term` (тихий drop). Задан — перед `Term` сообщение republish'ится в DLQ (best-effort). |

## Тюнинг poison-обработки

Три параметра определяют судьбу неуспешного stream-сообщения. Работают вместе:

| Цель | `NATS_JS_MAX_DELIVER` | `NATS_JS_NAK_BACKOFF` | `NATS_JS_DLQ_SUBJECT` |
|---|---|---|---|
| Бесконечный ретрай (доставка не теряется, риск «застрять») | `0` | по вкусу | — |
| Ограниченный ретрай + тихий drop | `> 0` (напр. `5`) | напр. `5s` | — |
| Ограниченный ретрай + dead-letter на разбор | `> 0` | напр. `5s` | напр. `dlq.orders` |

Логика на исчерпании попыток: `NumDelivered ≥ MaxDeliver` → (опц. publish в
`DLQSubject` с заголовками `Natskit-Dlq-Origin`/`Natskit-Dlq-Error`) → `Term`
(ределивери прекращается). До исчерпания — `NakWithDelay(NakBackoff)` или `Nak`.

Связь с `AckWait`: при `NakBackoff = 0` интервал между попытками определяется
`AckWait` (сервер ждёт ack, не дождавшись — переотправляет). `NakBackoff > 0`
задаёт явную задержку поверх этого — клиент сам просит вернуть позже.

## Backpressure-тюнинг

`MaxInFlight` — компромисс между пропускной способностью и потреблением памяти:
выше → больше параллельных обработчиков (выше throughput при I/O-bound
обработке), но больше одновременно живущих горутин и незакрытых ресурсов.
Для JetStream это же значение становится `MaxAckPending` консьюмера — потолком
числа неподтверждённых сообщений, которые сервер доставит до того, как начнёт
ждать ack'и. Слишком низкий `MaxAckPending` ограничит пропускную способность
даже при свободном CPU.

## Несколько инстансов

`natskit.Module(name)` с непустым именем читает те же переменные под префиксом
имени: `Module("hub")` → `HUB_NATS_URL`, `HUB_NATS_JETSTREAM_ENABLED`,
`HUB_NATS_JS_STREAM_NAMES` и т.д. (дефис и точка в имени становятся `_`).

Наборы инстансов изолированы: незаданная префиксная переменная берёт значение из
колонки Default, а **не** из одноимённой голой `NATS_*`. Инстанс, который только
публикует в стрим соседней команды, поэтому оставляет
`<PREFIX>_NATS_JS_STREAM_NAMES` незаданным — и не пытается создать у соседей свои
стримы.

## Прямая загрузка

`natskit.NewConfig()` / `natskit.NewConfigPrefixed(prefix)` читают переменные
выше из окружения. Через fx конфиг provide'ит сам `Module(name)` — приложению
достаточно подключить модуль.
