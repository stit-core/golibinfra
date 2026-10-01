# Тестирование natskit

Тесты кита — интеграционные (реальный NATS в testcontainers) плюс юнит-тесты
чистых функций и конфига. Под `-short` интеграционные пропускаются, без Docker —
падают (`testkit.RequireDocker`); юнит-тесты идут всегда.

## NATS-фикстуры

- `TestMain` пакета поднимает **один** контейнер `testkit.NATSImage` с `-js` на
  весь прогон; тест берёт его URL через `natsURL(t)` (внутри —
  `testkit.RequireDocker(t)`, поэтому под `-short` тест пропускается, а без
  Docker падает). Изоляцию даёт не отдельный сервер, а уникальные имена:
  `nameFor(t)` — стрим по имени теста, `subj(t, "created")` — subject внутри него.
- `testkit.StartNATS(t, testkit.WithJetStream())` — общая фикстура репо для
  тестов потребителя: контейнер с фиксированным тегом образа, снимается в
  `t.Cleanup`. Без `WithJetStream()` поднимается Core NATS: JetStream-вызовы на
  таком сервере возвращают ошибку.

```go
func TestSomething(t *testing.T) {
	stream := nameFor(t)                                 // имя стрима = имя теста
	sb := newStreamBus(t, jsConfig(stream))              // StreamBus на общем сервере
	conn := connect(t, natsURL(t))                       // nats.Connect + ExportConn, Close в t.Cleanup
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	// публикация в subj(t, "created")
}
```

`natskit.ExportConn(*nats.Conn) *Conn` (в `export_test.go`) оборачивает уже
открытое соединение в `*natskit.Conn` — путь для тестов кита, когда соединением
управляет сам тест; `ExportConnWith` добавляет логгер и `Config` (например,
`DrainTimeout`). Приложению соединение открывает `natskit.New(name, cfg, log)`
или `Module(name)`. `logkit.Nop()` гасит логи в тесте; `memLogger`
(`helpers_test.go`) собирает записи вместе с атрибутами — на нём держатся
проверки редактирования URL, дрейфа конфига стрима и мёртвых сообщений.

`jsConfig(streams...)` — конфиг JetStream с `StreamManaged: true`, `MaxAge: 1h`,
`AckWait: 5s`, `MaxInFlight: 32`; тесты правят нужные поля поверх.
`clientConfig(url)` — конфиг соединения для `NewConn`/`New` (короткие
`ReconnectWait`/`DrainTimeout`).

`startBlackholeProxy(t, target)` — TCP-прокси до контейнера: по флагу `drop`
отбрасывает ответы сервера (потерянный `PubAck`), `disconnect()` рвёт
соединения (reconnect), `stop()` закрывает и listener (исчерпание reconnect'ов).

Позитивные ассерты — на каналах и `eventually(...)`, а не на `time.Sleep`;
`expectSilence(...)` остаётся только там, где проверяется отсутствие доставки.

## Что покрыто тестами кита

| Тест | Проверяет |
|---|---|
| `TestBus_PublishSubscribe` / `…JSON` / `…QueueJSON` | round-trip Core publish/subscribe сырых байт и дженериков. |
| `TestBus_PublishHeadersReachHandler` | `WithHeader`/`WithMsgID` доезжают до обработчика через `HeadersFromContext`. |
| `TestBus_QueueGroupLoadBalances` | queue-группа балансирует — каждое сообщение ровно одному подписчику (нет дублей). |
| `TestBus_HandlerErrorLogged` | ошибка обработчика логируется с полем `instance`. |
| `TestSubscribeJSON_SkipsUndecodablePayload` | битый payload до обработчика не доходит. |
| `TestBus_SubscribeRejectsCancelledContext` | отменённый ctx регистрации → ошибка сразу. |
| `TestBus_PanicRecovery` | паника в обработчике не валит подписчик; следующее сообщение доходит. |
| `TestBus_Backpressure` | пиковая конкуренция обработчиков не превышает `MaxInFlight`. |
| `TestBus_HandlerTimeout` | Core-обработчик получает отмену ctx по `NATS_HANDLER_TIMEOUT`. |
| `TestBus_SubscriptionUnsubscribe` / `TestStreamBus_SubscriptionUnsubscribe` | после `Unsubscribe` сообщения не приходят (Core и JetStream). |
| `TestBus_SubscriptionDrainWaitsForHandler` / `…HonoursContext` | `Drain` ждёт работающий обработчик и снимает интерес; истёкший ctx → ошибка. |
| `TestStreamBus_SubscriptionDrainWaitsForHandler` | то же для JetStream-консьюмера. |
| `TestConn_CloseWaitsForInflightHandler` | `Close` возвращается после завершения обработчика; статус `CLOSED`; `Subscribe` после — `ErrClosed`. |
| `TestConn_CloseGivesUpAfterDrainTimeout` | зависший обработчик: `Close` укладывается в `DrainTimeout` и пишет предупреждение. |
| `TestConn_CloseCancelsHandlerContext` | по истечении бюджета drain обработчик получает отмену `ctx`. |
| `TestConn_CloseContextHonoursDeadline` | `CloseContext` укладывается в дедлайн ctx, даже когда `DrainTimeout` больше. |
| `TestClient_CloseWaitsForJetStreamHandler` | JetStream-обработчик успевает `Ack` до закрытия — новый инстанс с тем же durable сообщение не получает. |
| `TestConn_CloseIsIdempotentAndNilSafe` | повторный и nil-`Close` безопасны; `Healthy` после — `ErrConnectionClosed`. |
| `TestSubscribeOnStart_SubscribesOnStartDrainsOnStop` | подписка — в `OnStart` после зависимостей, объявленных раньше, drain — в `OnStop` до их остановки (без Docker). |
| `TestSubscribeOnStart_JetStreamDisabledFailsStart` | `ErrJetStreamDisabled` из stream-подписки валит старт (без Docker). |
| `TestConn_ReconnectLogsRedactedURL` | пароль из `NATS_URL` не попадает в лог переподключения. |
| `TestConn_ReconnectsExhaustedIsLoggedAndUnhealthy` | исчерпание reconnect'ов — `error`-запись и `ErrConnectionClosed` у пробы. |
| `TestClient_ProbeReflectsConnectionState` | проба зелёная на живом соединении и красная после `Close`. |
| `TestNewConn_RejectsInvalidConfig` / `TestNewConn_AuthFiles` | невалидный URL и битый nkey-seed валят старт; пароль не попадает в текст ошибки. |
| `TestBus_EmitsMetricsAndSpans` | эмитятся все 5 метрик `nats_*` и спаны `natskit.publish`/`natskit.handle`. |
| `TestBus_ConsumerSpanIsChildOfProducer` | consumer-спан — child producer-спана (trace-context через `nats.Header`). |
| `TestBus_PublishMetricSubjectIsOptIn` | label `subject` publish-метрик появляется только с `WithMetricSubject`. |
| `TestStreamBus_ConsumeMetricsUseFilterSubject` | label `subject` и имя consumer-спана — фильтр подписки, реальный subject — в атрибуте. |
| `TestStreamBus_Disabled` / `TestNewStreamBus_RejectsInvalidConfig` | выключенный JetStream отдаёт `ErrJetStreamDisabled` (и из `CheckStreams`); невалидный DLQ-subject — `ErrConfig`. |
| `TestStreamBus_CheckStreams` | фильтр, накрытый стримом целиком, проходит; пересечения мало — `ErrNoStream` с фильтром в тексте; без фильтров — только проверка JetStream. |
| `TestStreamBus_CheckStreamsServerWithoutJetStream` | сервер без `-js`: публикация — `ErrNoStreamResponse`, как без стрима, а `CheckStreams` — `ErrJetStreamDisabled`. |
| `TestStreamBus_PublishSubscribe` / `…WildcardSubjectInContext` / `…PublishHeadersReachHandler` | JetStream round-trip, `SubjectFromContext` под wildcard-фильтром, заголовки публикации. |
| `TestStreamBus_PublishBatchDedup` | дубль по `MsgID` стрим не сохраняет дважды; дубль — это `PubAck`, не ошибка. |
| `TestStreamBus_PublishBatchPartialError` | частичный сбой батча: валидные публикуются, oversized возвращает ошибку в `results[i]`. |
| `TestStreamBus_PublishBatchCancelledContext` | отмена ctx батча не превращает подтверждённые публикации в ошибки. |
| `TestStreamBus_PublishBatchLostAckTimesOut` | потерянный `PubAck` → `ErrAsyncPublishTimeout` в пределах `PublishAsyncTimeout`. |
| `TestStreamBus_MaxDeliverDLQ` | исчерпание `MaxDeliver` → `error`-лог + DLQ-копия с `Origin`/`Error`/`Reason`/`Stream`/`Consumer`/`Deliveries`/`Msg-Id`; после `Term` попыток ровно `MaxDeliver`. |
| `TestStreamBus_TermWithoutDLQ` | без `DLQSubject` отказ всё равно логируется, ределивери прекращается. |
| `TestStreamBus_TerminalErrorSkipsRetries` / `…DecodeErrorGoesToDLQ` | `ErrTerminal` и ошибка декода — DLQ с первой попытки, `reason=terminal`. |
| `TestStreamBus_PanicGoesPoisonPath` | паника обработчика идёт через NAK и заканчивается `Term`. |
| `TestStreamBus_NakBackoffDelaysRedelivery` | `NakBackoff` разносит попытки во времени. |
| `TestStreamBus_RejectsDLQUnderConsumerFilter` | фильтр, накрывающий DLQ-subject, отвергается на регистрации. |
| `TestStreamBus_DLQInsideStreamWithDedup` | DLQ внутри стрима с дедупом: `Nats-Msg-Id` снят и перенесён в `Natskit-Dlq-Msg-Id`. |
| `TestStreamBus_SlowHandlerNotRedelivered` | обработчик дольше `AckWait` (в пределах `HandlerTimeout`) вызывается один раз — `InProgress` продлевает ожидание сервера. |
| `TestStreamBus_StuckHandlerCancelled` | зависший обработчик получает `DeadlineExceeded` по `HandlerTimeout`; сообщение приходит повторно. |
| `TestStreamBus_EnsureStreamsCreatesMissing` / `…KeepsOperatorTuning` / `…UnmanagedStreams` | декларация стримов: создание, долив только `<name>.>`, режим `StreamManaged=false`. |
| `TestStreamBus_WarnsOnStreamConfigDrift` | расхождение конфига существующего стрима с ENV сообщается `warn`-записью. |
| `TestModule_*` | fx-граф: `Module` требует `Config` от caller'а, именованный инстанс, `HealthProbe`, невалидное имя инстанса, живой Start/Stop. |
| `TestNewConfig_*` / `TestConfig_Validate` | чтение ENV, дефолты, изоляция префиксов, табличная проверка `Validate` (URL, таймауты, файлы, JetStream-поля). |
| `TestStreamBus_DLQPublishFailureKeepsMessage` | DLQ-стрим отвергает копию (`discard=new`) — оригинал не терминируется, а возвращается на ределивери. |
| `TestKeepInProgress_*` | keepalive `+WPI`: транзиентная ошибка не выключает тикер (предупреждение — одно), подтверждённое сообщение и отмена `ctx` его останавливают. |
| `TestRedactURL`, `TestSubjectMatches`, `TestParseRetention`, `TestInflightGroup_*`, `TestLoadEnv_ParseErrors`, `TestCloseBudget`, `TestSettledResult` | юнит-тесты чистых функций — идут и под `-short`. |

## Тесты в коде потребителя

Для тестов бизнес-логики без Docker есть фейк шины
`testkit/natstest.Recorder` — реализует и `natskit.Bus`, и `natskit.StreamBus`,
доставляя сообщения подписчикам синхронно:

```go
r := natstest.New()
_, err := natskit.SubscribeStreamJSON(r, ctx, "ORDERS", "orders.>", "worker", handleOrder)

// «пришло сообщение из шины» — обработчик уже отработал, когда Deliver вернулся
// (сам Deliver публикацией не считается и в Published не попадает)
err = r.Deliver(ctx, "orders.created", []byte(`{"id":1}`),
	natskit.WithHeader("Event-Type", "order.created"))

// что опубликовал сервис: здесь — handleOrder, вызванный этим Deliver
got := r.PublishedOn("orders.confirmed")
if len(got) != 1 || got[0].MsgID != "order-1" {
	t.Fatalf("published = %+v", got)
}
if got[0].MetricSubject != "orders.*" {
	t.Fatalf("publish без WithMetricSubject заводит серию на каждый id: %+v", got[0])
}
```

`SetPublishError(err)` включает ветку «шина недоступна», `HandlerErrors()` —
ошибки, которые вернули обработчики, `Reset()` очищает записи между шагами.
`Message` несёт `Subject`, `Payload`, `Header`, `MsgID`, `MetricSubject` и
`JetStream` — то же, что уехало бы в реальную шину.

Для интеграционного покрытия доставки — `testkit.StartNATS(t,
testkit.WithJetStream())`, как выше, или `testkit.StartNATS(t)` для
Core-сценариев.

## Запуск

```bash
go test -short ./natskit/...   # быстрые тесты (integration пропускаются)
go test -race ./natskit/...    # полный прогон (нужен Docker для testcontainers)
```
