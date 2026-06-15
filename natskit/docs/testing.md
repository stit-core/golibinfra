# Тестирование natskit

Тесты кита — интеграционные: они поднимают реальный NATS-сервер в
testcontainers. Под `-short` все integration-тесты пропускаются (`t.Skip`).

## NATS-фикстура через testkit

`testkit.StartNATS(t)` поднимает контейнер `nats:latest`, ждёт лог
`Server is ready` и возвращает `*NATSFixture{ URL, Container }`. Контейнер
останавливается в `t.Cleanup`. Это Core-NATS (без JetStream).

```go
func TestSomething(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	fix := testkit.StartNATS(t)

	nc, err := nats.Connect(fix.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	conn := natskit.ExportConn(nc) // *nats.Conn → *natskit.Conn
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	// ...
}
```

`natskit.ExportConn(*nats.Conn) *Conn` (в `export_test.go`) оборачивает уже
открытое соединение в `*natskit.Conn` — это путь без fx-хука `newConn`.
`logkit.Nop()` гасит логи в тесте.

## JetStream в тестах

`testkit.StartNATS` поднимает NATS без JetStream. Для `StreamBus` нужен
контейнер с флагом `-js` — кит поднимает его в `jetstream_test.go`
(`startJetStreamNATS`): `nats:latest`, `Cmd: ["-js"]`. Дальше — обычный
`nats.Connect` + `ExportConn` + `NewStreamBus`:

```go
url := startJetStreamNATS(t)
nc, _ := nats.Connect(url)
t.Cleanup(nc.Close)
conn := natskit.ExportConn(nc)

cfg := natskit.Config{
	JetStream: natskit.JetStreamConfig{
		Enabled:     true,
		StreamNames: []string{"ORDERS"},
		Retention:   "limits",
		MaxAge:      time.Hour,
		MaxInFlight: 32,
		AckWait:     5 * time.Second,
	},
}
sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
```

## Что покрыто тестами кита

| Тест | Проверяет |
|---|---|
| `TestBus_PublishSubscribe` / `…JSON` | round-trip Core publish/subscribe; декод дженериков. |
| `TestBus_QueueGroupLoadBalances` | queue-группа балансирует — каждое сообщение ровно одному подписчику (нет дублей). |
| `TestBus_PanicRecovery` | паника в обработчике не валит подписчик; следующее сообщение доходит. |
| `TestBus_Backpressure` | пиковая конкуренция обработчиков не превышает `MaxInFlight`. |
| `TestBus_EmitsMetricsAndSpans` | эмитятся все 5 метрик `nats_*` и спаны `natskit.publish`/`natskit.handle` (in-memory OTel SDK). |
| `TestStreamBus_Disabled` | при `Enabled=false` все методы `StreamBus` отдают `ErrJetStreamDisabled`. |
| `TestStreamBus_PublishSubscribe` / `…WildcardSubjectInContext` | JetStream round-trip; `SubjectFromContext` отдаёт конкретный субъект под wildcard-фильтром. |
| `TestStreamBus_PublishBatchDedup` | дубль по `MsgID` стрим не сохраняет дважды; дубль — это `PubAck`, не ошибка. |
| `TestStreamBus_PublishBatchPartialError` | частичный сбой батча: валидные публикуются, oversized возвращает ошибку в `results[i]`. |
| `TestStreamBus_MaxDeliverDLQ` | исчерпание `MaxDeliver` → DLQ-сообщение с `Natskit-Dlq-Origin`/`Natskit-Dlq-Error`; после `Term` попыток ровно `MaxDeliver`. |

## Тесты в коде потребителя

Для unit-тестов бизнес-логики мокайте `Bus`/`StreamBus` — это интерфейсы, замена
тривиальна (своя структура с нужными методами). Для интеграционного покрытия
доставки — `testkit.StartNATS` (Core) или контейнер с `-js` (JetStream), как
выше.

## Запуск

```bash
go test -short ./natskit/...   # быстрые тесты (integration пропускаются)
go test -race ./natskit/...    # полный прогон (нужен Docker для testcontainers)
```
