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

Идиоматичный путь — `natskit.Module(name)`: fx поднимает инстанс шины (`*Client`)
и provide'ит его `Config`, `Bus` и `StreamBus`; приложение инжектит нужный bus.
Пустое имя — дефолтный инстанс из `NATS_*`; инстансов может быть несколько (своя
шина и внешняя — см. [docs/usage.md](docs/usage.md)).

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
		natskit.Module(""), // конфиг из NATS_*, Client + Bus + StreamBus
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

Минимальный ENV: `NATS_URL`. Сигнатуры дженериков, JetStream, DLQ/NAK и рецепт
двух шин в одном приложении — в [docs/usage.md](docs/usage.md). Без fx инстанс
собирается напрямую: `natskit.New(name, cfg, log)` (см. также
[docs/testing.md](docs/testing.md)).

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство `Bus`/`StreamBus`, consumer reliability, queue-группы, trace-propagation, проектные решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV + тюнинг backpressure / DLQ / `MaxDeliver` / NAK-backoff / retention.
- [docs/observability.md](docs/observability.md) — метрики, labels, спаны (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: fx Module, несколько шин в одном приложении, `PublishJSON`/`SubscribeJSON`, JetStream, DLQ/NAK, чтение subject/headers из ctx.
- [docs/testing.md](docs/testing.md) — testcontainers NATS через `testkit.StartNATS`, JetStream-контейнер, `ExportConn`.
