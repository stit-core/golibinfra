# Тестирование natskit

Тесты кита — интеграционные: они поднимают реальный NATS-сервер в
testcontainers. Под `-short` все integration-тесты пропускаются (`t.Skip`).

## NATS-фикстуры

- `startJetStreamNATS(t)` (`helpers_test.go`) — контейнер `nats:2.14.6-alpine` с
  флагом `-js`, ждёт лог `Server is ready`, возвращает URL; контейнер
  останавливается в `t.Cleanup`. Подходит и Core-тестам. Тег образа
  фиксирован — прогон не зависит от того, что сегодня отдаёт registry.
- `testkit.StartNATS(t)` — общая фикстура репо (Core NATS, без JetStream);
  образ и тег задаёт `testkit`.

```go
func TestSomething(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)                              // nats.Connect + ExportConn, Close в t.Cleanup
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), jsConfig("ORDERS"))
	// ...
}
```

`natskit.ExportConn(*nats.Conn) *Conn` (в `export_test.go`) оборачивает уже
открытое соединение в `*natskit.Conn` — путь для тестов кита, когда соединением
управляет сам тест; `ExportConnWith` добавляет логгер и `Config` (например,
`DrainTimeout`). Приложению соединение открывает `natskit.New(name, cfg, log)`
или `Module(name)`. `logkit.Nop()` гасит логи в тесте; `memLogger`
(`helpers_test.go`) собирает записи для проверки предупреждений.

`jsConfig(streams...)` — конфиг JetStream с `StreamManaged: true`, `MaxAge: 1h`,
`AckWait: 5s`, `MaxInFlight: 32`; тесты правят нужные поля поверх.
`clientConfig(url)` — конфиг соединения для `NewConn`/`New` (короткие
`ReconnectWait`/`DrainTimeout`).

`startBlackholeProxy(t, target)` — TCP-прокси до контейнера, по флагу `drop`
отбрасывающий ответы сервера: имитация потерянного `PubAck`.

## Что покрыто тестами кита

| Тест | Проверяет |
|---|---|
| `TestBus_PublishSubscribe` / `…JSON` | round-trip Core publish/subscribe; декод дженериков. |
| `TestBus_QueueGroupLoadBalances` | queue-группа балансирует — каждое сообщение ровно одному подписчику (нет дублей). |
| `TestBus_SubscribeRejectsCancelledContext` | отменённый ctx регистрации → ошибка сразу. |
| `TestBus_PanicRecovery` | паника в обработчике не валит подписчик; следующее сообщение доходит. |
| `TestBus_Backpressure` | пиковая конкуренция обработчиков не превышает `MaxInFlight`. |
| `TestBus_SubscriptionUnsubscribe` | после `Unsubscribe` сообщения не приходят. |
| `TestBus_SubscriptionDrainWaitsForHandler` / `…HonoursContext` | `Drain` ждёт работающий обработчик и снимает интерес; истёкший ctx → ошибка. |
| `TestStreamBus_SubscriptionDrainWaitsForHandler` | то же для JetStream-консьюмера. |
| `TestConn_CloseWaitsForInflightHandler` | `Close` возвращается после завершения обработчика; статус `CLOSED`; `Subscribe` после — `ErrClosed`. |
| `TestConn_CloseGivesUpAfterDrainTimeout` | зависший обработчик: `Close` укладывается в `DrainTimeout`, пишет предупреждение, закрывает соединение. |
| `TestClient_CloseWaitsForJetStreamHandler` | JetStream-обработчик успевает `Ack` до закрытия — новый инстанс с тем же durable сообщение не получает. |
| `TestConn_CloseIsIdempotentAndNilSafe` | повторный и nil-`Close` безопасны. |
| `TestBus_EmitsMetricsAndSpans` | эмитятся все 5 метрик `nats_*` и спаны `natskit.publish`/`natskit.handle` (in-memory OTel SDK). |
| `TestStreamBus_ConsumeMetricsUseFilterSubject` | label `subject` consume-метрик JetStream — фильтр подписки, не реальный subject. |
| `TestStreamBus_Disabled` | при `Enabled=false` все методы `StreamBus` отдают `ErrJetStreamDisabled`. |
| `TestStreamBus_PublishSubscribe` / `…WildcardSubjectInContext` | JetStream round-trip; `SubjectFromContext` отдаёт конкретный субъект под wildcard-фильтром. |
| `TestStreamBus_PublishBatchDedup` | дубль по `MsgID` стрим не сохраняет дважды; дубль — это `PubAck`, не ошибка. |
| `TestStreamBus_PublishBatchPartialError` | частичный сбой батча: валидные публикуются, oversized возвращает ошибку в `results[i]`. |
| `TestStreamBus_PublishBatchLostAckTimesOut` | потерянный `PubAck` (прокси отбрасывает ответы) → `ErrAsyncPublishTimeout` в пределах `PublishAsyncTimeout`. |
| `TestStreamBus_MaxDeliverDLQ` | исчерпание `MaxDeliver` → DLQ-сообщение с `Natskit-Dlq-Origin`/`Natskit-Dlq-Error` (без переводов строк); после `Term` попыток ровно `MaxDeliver`. |
| `TestStreamBus_DLQInsideStreamWithDedup` | DLQ-subject внутри стрима с окном дедупа: копия сохраняется, `Nats-Msg-Id` снят и перенесён в `Natskit-Dlq-Msg-Id`. |
| `TestStreamBus_SlowHandlerNotRedelivered` | обработчик дольше `AckWait` (в пределах `HandlerTimeout`) вызывается один раз — `InProgress` продлевает ожидание сервера. |
| `TestStreamBus_StuckHandlerCancelled` | зависший обработчик получает `DeadlineExceeded` по `HandlerTimeout`; сообщение приходит повторно. |
| `TestStreamBus_EnsureStreamsCreatesMissing` | отсутствующий стрим создаётся с конфигом кита. |
| `TestStreamBus_EnsureStreamsKeepsOperatorTuning` | существующий стрим: доливается только `<name>.>`; `MaxBytes`/`Duplicates`/описание/чужие subjects сохранены; повторный старт идемпотентен. |
| `TestStreamBus_UnmanagedStreams` | `StreamManaged=false`: отсутствующий стрим → ошибка старта; существующий не меняется. |
| `TestModule_*` | fx-граф с дефолтным и именованным инстансами; `HealthProbe(name)` и `healthkit.Provide(natskit.Probe)` кладут `healthkit.Probe` в группу `health`. |
| `TestNewConfig_*` | чтение ENV, дефолты (в т.ч. `DrainTimeout`, `StreamManaged`, `PublishAsyncTimeout`, `HandlerTimeout`), изоляция префиксов, ошибка на невалидном значении. |

## Тесты в коде потребителя

Для unit-тестов бизнес-логики мокайте `Bus`/`StreamBus` — это интерфейсы, замена
тривиальна (своя структура с нужными методами; `Subscribe*` возвращают
`Subscription` — в моке достаточно `nil, nil`). Для интеграционного покрытия
доставки — контейнер NATS с `-js`, как выше, или `testkit.StartNATS` (Core).

## Запуск

```bash
go test -short ./natskit/...   # быстрые тесты (integration пропускаются)
go test -race ./natskit/...    # полный прогон (нужен Docker для testcontainers)
```
