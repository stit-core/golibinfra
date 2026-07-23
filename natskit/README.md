# natskit

Pub/sub поверх [NATS](https://github.com/nats-io/nats.go): интерфейс `Bus`
(Core NATS — fire-and-forget fan-out + load-balanced queue-группы) и `StreamBus`
(JetStream — персист, дедуп по `Nats-Msg-Id`, durable-консьюмеры с
poison-обработкой DLQ/`MaxDeliver`/NAK-backoff). Типизированные helper'ы
`PublishJSON[T]`/`SubscribeJSON[T]` (и stream-варианты) убирают ручной
marshal/unmarshal. Из коробки: panic recovery в обработчиках, backpressure через
семафор (`NATS_MAX_IN_FLIGHT`), W3C trace-propagation через `nats.Header`
(consumer-спан — child producer-спана) и метрики `nats_*`.

Кит даёт сервису транспорт и надёжность доставки; доменная логика
(идемпотентность по бизнес-ключу, схемы сообщений, роутинг) — на стороне
потребителя.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/natskit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

Идиоматичный путь — `natskit.Module`: fx сам поднимает соединение (`*Conn`),
`Bus` и `StreamBus`; приложение provide'ит только `Config` и инжектит `Bus`.

```go
package main

import (
	"context"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

type OrderCreated struct {
	ID int `json:"id"`
}

func main() {
	fx.New(
		logkit.Module,
		fx.Provide(natskit.NewConfig), // читает NATS_URL и пр. из ENV
		natskit.Module,                // *Conn + Bus + StreamBus
		fx.Invoke(run),
	).Run()
}

func run(bus natskit.Bus) error {
	ctx := context.Background()

	// Типобезопасный подписчик: payload декодится в OrderCreated автоматически.
	if err := natskit.SubscribeJSON(bus, ctx, "orders.created",
		func(ctx context.Context, m OrderCreated) error {
			return nil // бизнес-обработка
		}); err != nil {
		return err
	}

	// Публикация: msg маршалится в JSON, trace-context инжектится в header'ы.
	return natskit.PublishJSON(bus, ctx, "orders.created", OrderCreated{ID: 42})
}
```

Минимальный ENV: `NATS_URL`. Сигнатуры дженериков, JetStream и DLQ/NAK — в
[docs/usage.md](docs/usage.md). Прямой `NewBus(conn, log, cfg)` без fx тоже
доступен (см. [docs/testing.md](docs/testing.md)), но менее идиоматичен.

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство `Bus`/`StreamBus`, consumer reliability, queue-группы, trace-propagation, проектные решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV + тюнинг backpressure / DLQ / `MaxDeliver` / NAK-backoff / retention.
- [docs/observability.md](docs/observability.md) — метрики, labels, спаны (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: fx Module, `PublishJSON`/`SubscribeJSON`, JetStream, DLQ/NAK, чтение subject/headers из ctx.
- [docs/testing.md](docs/testing.md) — testcontainers NATS через `testkit.StartNATS`, JetStream-контейнер, `ExportConn`.
