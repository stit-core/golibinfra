# grpckit

Серверный gRPC-примитив (аналог `httpkit` для gRPC): `*grpc.Server` с цепочкой
интерсепторов (request-id — общий с `httpkit` ключ, UUID v7 при
отсутствии/невалидном входящем id, эхо в response-header; access-log с уровнем по
gRPC-коду; recovery; опц. auth через порт `Authenticator` с обезличенным
`Unauthenticated` наружу), observability через `otelgrpc` StatsHandler
(`rpc.server.*`), встроенные health- и (опц.) reflection-сервисы. Доменные
gRPC-сервисы регистрируются в fx value-group `grpckit.services` (`grpckit.ProvideService`) — кит владеет
транспортом, интерсепторами и lifecycle, а gen-стабы и бизнес-логика живут в
сервис-репо.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/grpckit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

Регистрация доменного сервиса — `grpckit.ProvideService` в fx value-group `grpckit.services`:
потребитель отдаёт `grpckit.ServiceRegistrar` (`func(*grpc.Server)`), кит
навешивает его на сервер до `Serve`. `Module` сам форсит конструирование сервера
— зависеть от `*grpckit.Server` в графе не нужно.

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
		fx.Provide(logkit.NewConfig), // Config логгера из ENV
		grpckit.Module,
		fx.Provide(grpckit.NewConfig), // Config сервера из ENV
		fx.Provide(newGreeterServer),
		grpckit.ProvideService(newGreeterRegistrar),
	).Run()
}
```

Минимальный ENV: ничего — `GRPC_ADDR` по умолчанию `:9090`. Auth, reflection,
mTLS, свои интерсепторы и прямой (не-fx) `New` — см. [docs/usage.md](docs/usage.md).

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство: сервер, цепочка интерсепторов, StatsHandler, fx-group сервисов, ключевые решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник `Config`/ENV: bind, лимиты сообщений, keepalive, TLS, health, reflection, правила `Validate`.
- [docs/observability.md](docs/observability.md) — метрики `rpc.server.*`, спаны `otelgrpc`, request-id и уровни access-log (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: регистрация сервиса через fx-group, `Authenticator`-порт, request-id, health-пробы, точки расширения, reflection toggle.
- [docs/testing.md](docs/testing.md) — `testkit/grpctest`: bufconn-харнесс для тестов потребителей.

## API

| Символ | Сигнатура | Назначение |
| --- | --- | --- |
| `Module` | `fx.Options` | `*Server` + lifecycle (listen, serve, graceful stop); `fx.Invoke` гарантирует старт. |
| `ServicesGroup` | `const = "grpckit.services"` | Имя fx value-group регистраторов доменных сервисов. |
| `ProvideService(ctor, anns...)` | `fx.Option` | Конструктор `ServiceRegistrar` (или `[]ServiceRegistrar`) в группу `ServicesGroup`; другой тип результата — ошибка сборки графа. |
| `NewConfig` | `func() (Config, error)` | `Config` из ENV + `Validate`. |
| `Config.Validate` | `func() error` | Проверка контракта конфига; ошибка оборачивает `ErrConfig`. |
| `New` | `func(Config, logkit.Logger, ...ServerOption) (*Server, error)` | Сборка сервера без прослушивания порта. |
| `Server.Serve` / `Server.Stop` | `func(net.Listener) error` / `func(context.Context)` | Старт и graceful-остановка в пределах `GRPC_SHUTDOWN_GRACE`. |
| `Server.Addr` | `func() string` | Фактический адрес слушателя (порт `0` в тестах). |
| `Server.GRPC` | `func() *grpc.Server` | Нижележащий сервер (поздняя регистрация, интероп). |
| `Server.SetServing` | `func(service string, ok bool)` | Ручной health-статус сервиса (`""` — overall). |
| `ServiceRegistrar` | `func(*grpc.Server)` | Порт регистрации доменного сервиса. |
| `Authenticator` | `interface{ Authenticate(ctx, token) (context.Context, error) }` | Порт аутентификации (реализует потребитель). |
| `WithAuthenticator`, `WithService`, `WithHealthProbes` | `ServerOption` | Auth-порт, регистрация сервиса и health-пробы вне fx. |
| `WithUnaryInterceptors`, `WithStreamInterceptors` | `ServerOption` | Свои интерсепторы в хвост цепочки кита. |
| `WithServerOptions`, `WithStatsHandlerOptions` | `ServerOption` | Произвольные `grpc.ServerOption` (mTLS, codec) и опции `otelgrpc`. |
| `RequestIDFrom`, `HeaderRequestID` | `func(context.Context) string`, `const` | Request-id вызова и его metadata-ключ. |
| `ErrConfig` | `error` | Sentinel невалидной конфигурации. |

## Инварианты

- `Module` стартует сервер сам: `fx.Invoke` внутри модуля не даёт графу
  «забыть» про транспорт, если от `*Server` никто не зависит.
- `Config` с нулевыми полями безопасен: лимиты, keepalive, грейс и дедлайн
  health-пробы получают дефолты; отрицательные значения и TLS с одной половиной
  пары cert/key — `ErrConfig` на старте.
- Reflection выключен по умолчанию (раскрывает схему API и exempt от auth).
- Health и reflection проходят без auth; всё остальное — под `Authenticator`,
  если он есть в графе.
