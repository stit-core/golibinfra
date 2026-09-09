# natskit

Pub/sub поверх [NATS](https://github.com/nats-io/nats.go): интерфейс `Bus`
(Core NATS — fire-and-forget fan-out + load-balanced queue-группы) и `StreamBus`
(JetStream — персист, дедуп по `Nats-Msg-Id`, durable-консьюмеры с
poison-обработкой DLQ/`MaxDeliver`/NAK-backoff, deadline + `InProgress` для
медленных обработчиков). Типизированные helper'ы `PublishJSON[T]`/
`SubscribeJSON[T]` (и stream-варианты) убирают ручной marshal/unmarshal.
Из коробки: handle подписки (`Subscription` — `Unsubscribe`/`Drain`), graceful
`Close` с ожиданием обработчиков (`NATS_DRAIN_TIMEOUT`), deadline обработчика
(`NATS_HANDLER_TIMEOUT`), panic recovery, backpressure через семафор
(`NATS_MAX_IN_FLIGHT`), логирование событий соединения (slow consumer,
disconnect/reconnect, исчерпание reconnect'ов) с редактированием userinfo в URL,
W3C trace-propagation через `nats.Header` (consumer-спан — child producer-спана)
и метрики `nats_*`. Аутентификация — креды в `NATS_URL`, `NATS_CREDS_FILE`,
`NATS_NKEY_SEED_FILE`, TLS-корень `NATS_TLS_CA_FILE`.

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
и provide'ит `Bus` и `StreamBus`; конфиг регистрирует приложение
(`fx.Provide(natskit.NewConfig)`), как у остальных китов. Пустое имя — дефолтный
инстанс из `NATS_*`; инстансов может быть несколько (своя шина и внешняя — см.
[docs/usage.md](docs/usage.md)). `OnStop`-хук модуля делает graceful `Close`.

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
		fx.Provide(logkit.NewConfig, natskit.NewConfig), // конфиги — на стороне приложения
		natskit.Module(""),                              // Client + Bus + StreamBus из NATS_*
		fx.Invoke(run),
	).Run()
}

func run(bus natskit.Bus) error {
	ctx := context.Background()

	// Типобезопасный подписчик: payload декодится в OrderCreated автоматически.
	// Subscription позволяет остановить подписку раньше Close; хранить не обязательно.
	if _, err := natskit.SubscribeJSON(bus, ctx, "orders.created",
		func(ctx context.Context, m OrderCreated) error {
			return nil // бизнес-обработка
		}); err != nil {
		return err
	}

	// Публикация: msg маршалится в JSON, trace-context инжектится в header'ы.
	return natskit.PublishJSON(bus, ctx, "orders.created", OrderCreated{ID: 42})
}
```

Минимальный ENV: `NATS_URL`. Конфиг валидируется на старте (`Validate` →
`ErrConfig`): пустой URL, отрицательный таймаут или недоступный файл кредов валят
запуск, а не первую публикацию. Сигнатуры дженериков, JetStream, DLQ/NAK,
`Subscription`, graceful shutdown и рецепт двух шин в одном приложении — в
[docs/usage.md](docs/usage.md). Без fx инстанс собирается напрямую:
`natskit.New(name, cfg, log)` (см. также [docs/testing.md](docs/testing.md)).

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство `Bus`/`StreamBus`, consumer reliability (deadline + `InProgress`, DLQ), декларация стримов, `Subscription` и graceful shutdown, trace-propagation, проектные решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV + тюнинг backpressure / DLQ / `MaxDeliver` / NAK-backoff / `AckWait` vs `HandlerTimeout` / управляемые стримы.
- [docs/observability.md](docs/observability.md) — метрики, labels, спаны, логи (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: fx Module, несколько шин в одном приложении, `PublishJSON`/`SubscribeJSON`, JetStream, DLQ/NAK, остановка подписки, чтение subject/headers из ctx.
- [docs/testing.md](docs/testing.md) — testcontainers NATS с JetStream (один контейнер на пакет), фейк шины `testkit/natstest` для тестов потребителя, helpers тестов.
