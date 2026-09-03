# grpckit

Серверный gRPC-примитив (аналог `httpkit` для gRPC): `*grpc.Server` с цепочкой
интерсепторов (recovery, request-id — общий с `httpkit` ключ, UUID v7 при
отсутствии/невалидном входящем id, эхо в response-header; access-log с уровнем по
gRPC-коду; опц. auth через порт `Authenticator` с обезличенным `Unauthenticated`
наружу), observability через `otelgrpc` StatsHandler (`rpc.server.*`),
встроенные health- и (опц.) reflection-сервисы. Доменные gRPC-сервисы
регистрируются через fx value-group `grpc.services` — кит владеет транспортом,
интерсепторами и lifecycle, а gen-стабы и бизнес-логика живут в сервис-репо.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/grpckit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

Регистрация доменного сервиса через fx value-group `grpc.services`: потребитель
отдаёт `grpckit.ServiceRegistrar` (`func(*grpc.Server)`), кит навешивает его на
сервер до `Serve`.

```go
package main

import (
	"google.golang.org/grpc"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/grpckit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// newGreeterRegistrar отдаёт стаб во fx-group; реализация — в сервис-репо.
func newGreeterRegistrar(svc *greeterServer) grpckit.ServiceRegistrar {
	return func(s *grpc.Server) { greeterpb.RegisterGreeterServer(s, svc) }
}

func main() {
	fx.New(
		logkit.Module,
		grpckit.Module,
		fx.Provide(grpckit.NewConfig), // Config из ENV (envconfig)
		fx.Provide(newGreeterServer),
		fx.Provide(fx.Annotate(
			newGreeterRegistrar,
			fx.ResultTags(`group:"grpc.services"`),
		)),
	).Run()
}
```

Минимальный ENV: ничего — `GRPC_ADDR` по умолчанию `:9090`. Auth, reflection и
прямой (не-fx) `New` — см. [docs/usage.md](docs/usage.md).

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство: сервер, цепочка интерсепторов, StatsHandler, fx-group сервисов, ключевые решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник `Config`/ENV: bind, лимиты сообщений, keepalive, TLS, health, reflection.
- [docs/observability.md](docs/observability.md) — метрики `rpc.server.*`, спаны `otelgrpc`, request-id и уровни access-log (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: регистрация сервиса через fx-group, `Authenticator`-порт, request-id, health-статус, reflection toggle.
- [docs/testing.md](docs/testing.md) — `testkit/grpctest`: bufconn-харнесс для тестов потребителей.
