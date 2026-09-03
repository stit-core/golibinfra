# Использование grpckit

## Регистрация сервиса через fx-group `grpc.services`

Идиоматичный путь. Доменный сервис отдаёт `ServiceRegistrar`
(`func(*grpc.Server)`) во value-group `grpc.services`; `grpckit.Module` собирает
все стабы и навешивает их до `Serve`.

```go
func newGreeterRegistrar(svc *greeterServer) grpckit.ServiceRegistrar {
	return func(s *grpc.Server) { greeterpb.RegisterGreeterServer(s, svc) }
}

fx.New(
	logkit.Module,
	grpckit.Module,
	fx.Provide(grpckit.NewConfig),
	fx.Provide(newGreeterServer),
	fx.Provide(fx.Annotate(
		newGreeterRegistrar,
		fx.ResultTags(`group:"grpc.services"`),
	)),
)
```

Несколько сервисов — несколько провайдеров с тем же тегом
`group:"grpc.services"`; порядок применения = порядок в графе.

## Authenticator-порт

Auth опционален. Реализуй порт `Authenticator` в сервис-репо (валидация
bearer-токена + обогащение `ctx` identity) и положи его в fx-граф — `Module`
поднимет `Authenticator` (`optional:"true"`) и навесит auth-интерсептор.

```go
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (context.Context, error)
}
```

Контракт:

- Токен берётся из metadata-ключа `authorization`; префикс `Bearer ` снимается
  (кейс-инсенситивно), голый токен тоже принимается.
- Пустой токен → `codes.Unauthenticated` («missing bearer token») до вызова порта.
- Ошибка `Authenticate` → `codes.Unauthenticated` с обезличенным текстом
  `unauthenticated`; сама ошибка порта пишется в лог на `Warn`
  (`grpc auth rejected`: `method`, `error`) и клиенту не уходит.
- Успех → `ctx`, возвращённый портом, передаётся в handler (так identity доезжает
  до бизнес-логики).
- Методы health (`/grpc.health.v1.Health/`) и reflection (`/grpc.reflection.`) —
  exempt: проходят без auth.

```go
fx.Provide(func(d *accessDomain) grpckit.Authenticator { return d }) // в графе → auth включён
```

Вне fx — опция конструктора:

```go
srv, err := grpckit.New(cfg, log, grpckit.WithAuthenticator(myAuth))
```

## Request-id

Клиент может прислать metadata `x-request-id` (печатный ASCII, до 128 символов)
— сервер сохранит его; иначе сгенерирует UUID v7. Id доступен handler'у через
ctx (тот же ключ, что у `httpkit`: `httpkit.RequestIDFrom(ctx)`), попадает в
`request_id` логов `logkit` и возвращается клиенту в response-header
`x-request-id`.

## Прямой `New` (тесты, не-fx сборка)

```go
srv, err := grpckit.New(cfg, log,
	grpckit.WithService(func(s *grpc.Server) { greeterpb.RegisterGreeterServer(s, svc) }),
)
if err != nil { /* … */ }

ln, _ := net.Listen("tcp", cfg.Addr)
go srv.Serve(ln)     // блокирующе обслуживает соединения
defer srv.Stop(ctx)  // graceful в пределах ShutdownGrace
```

`Server.GRPC()` отдаёт нижележащий `*grpc.Server` — для поздней регистрации
сервисов или интеропа.

## Health-статус

Health-сервис поднят всегда. Overall (`""`) переключается автоматически:
`SERVING` при `Serve`, `NOT_SERVING` при `Stop`. Per-service статус — вручную, по
готовности зависимостей:

```go
srv.SetServing("greeter.Greeter", true)  // SERVING
srv.SetServing("greeter.Greeter", false) // NOT_SERVING (например, потеряли БД)
```

Клиент проверяет статус через стандартный `grpc.health.v1.Health/Check`.

## Reflection toggle

Reflection выключен по умолчанию; включается `GRPC_REFLECTION=true`
(`Config.Reflection`). Удобен для `grpcurl`/отладки в dev. В prod держим
выключенным — он раскрывает список методов и при этом exempt от auth.

## TLS

TLS включается заданием **обоих** `GRPC_TLS_CERT_FILE` и `GRPC_TLS_KEY_FILE`. Без
пары сервер слушает plaintext (типично, когда TLS терминируется на mesh/ingress).
Подробности — [configuration.md](configuration.md).
