# Использование grpckit

## Регистрация сервиса через fx-group `grpckit.services`

Идиоматичный путь. Доменный сервис отдаёт `ServiceRegistrar`
(`func(*grpc.Server)`) через `grpckit.ProvideService` во value-group `grpckit.services`
(константа `grpckit.ServicesGroup`); `grpckit.Module` собирает все стабы и навешивает их до
`Serve`.

```go
func newGreeterRegistrar(svc *greeterServer) grpckit.ServiceRegistrar {
	return func(s *grpc.Server) { greeterpb.RegisterGreeterServer(s, svc) }
}

fx.New(
	logkit.Module,
	fx.Provide(logkit.NewConfig),
	grpckit.Module,
	fx.Provide(grpckit.NewConfig),
	fx.Provide(newGreeterServer),
	grpckit.ProvideService(newGreeterRegistrar),
)
```

Несколько сервисов — несколько `ProvideService` (или один конструктор
`[]ServiceRegistrar`); конструктор другого типа — ошибка сборки графа, а не молча
пропавший сервис. Зависеть от
`*grpckit.Server` не нужно: `Module` форсит его конструирование сам.

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
- Пустой токен (в том числе заголовок из одного префикса `Bearer `) →
  `codes.Unauthenticated` («missing bearer token») до вызова порта.
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
`grpckit.RequestIDFrom(ctx)` (ключ metadata — `grpckit.HeaderRequestID`), попадает
в поле `request_id` логов `logkit` и возвращается клиенту в response-header
`x-request-id`. Ключ ctx общий с `httpkit`, поэтому HTTP→gRPC hop сохраняет один
идентификатор; исходящие вызовы `grpcclient` продолжают ту же цепочку.

## Health-статус

Health-сервис поднят всегда. Overall (`""`) переключается автоматически:
`SERVING` при `Serve`, `NOT_SERVING` при `Stop`. Per-service статус — вручную, по
готовности зависимостей:

```go
srv.SetServing("greeter.Greeter", true)  // SERVING
srv.SetServing("greeter.Greeter", false) // NOT_SERVING (например, потеряли БД)
```

Клиент проверяет статус через стандартный `grpc.health.v1.Health/Check`.

### Health по пробам зависимостей

Пробы `healthkit.Probe` из fx-группы `healthkit.probes` (их отдают `pgkit`, `natskit`,
`cachekit`, `storagekit` и сервис-репо через `healthkit.ProvideProbe`) переключают
overall-статус автоматически: каждые `GRPC_HEALTH_INTERVAL` кит прогоняет пробы и
ставит `SERVING`/`NOT_SERVING`. Ничего подключать не нужно — `Module` читает ту
же группу, что `/ready` в `httpkit`:

```go
fx.New(
	grpckit.Module,
	fx.Provide(grpckit.NewConfig),
	pgkit.Module, // отдаёт свою пробу в группу health
	healthkit.ProvideProbe(func(c *myDep) healthkit.Probe {
		return healthkit.New("mydep", c.Ping)
	}),
)
```

У каждой пробы свой дедлайн — `GRPC_HEALTH_PROBE_TIMEOUT` (`5s`): зависшая
зависимость даёт `NOT_SERVING` по таймауту, а не растягивает цикл на период
следующего тика.

`GRPC_HEALTH_INTERVAL=0` выключает переоценку (тогда статус меняет только
`SetServing`). Вне fx пробы передаются опцией `grpckit.WithHealthProbes(...)`.

## Точки расширения

Кит не закрывает доступ к `grpc.Server`: всё, чего нет в `Config`, добавляется
опциями `New` (и через них — fx-конструктором).

```go
srv, err := grpckit.New(cfg, log,
	// mTLS: клиентские сертификаты проверяет сам сервис, не mesh.
	grpckit.WithServerOptions(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}))),
	// Свои интерсепторы — вплотную к handler'у, после auth.
	grpckit.WithUnaryInterceptors(rateLimitUnary(limiter)),
	grpckit.WithStreamInterceptors(tenantStream(resolver)),
	// Опции otelgrpc: не трейсить health-пробы оркестратора.
	grpckit.WithStatsHandlerOptions(otelgrpc.WithFilter(func(i *stats.RPCTagInfo) bool {
		return !strings.HasPrefix(i.FullMethodName, "/grpc.health.v1.Health/")
	})),
)
```

- `WithServerOptions` применяется последним — одноимённая опция потребителя
  побеждает опцию кита (например, свой `grpc.Creds` поверх `GRPC_TLS_*`).
- `WithUnaryInterceptors`/`WithStreamInterceptors` видят аутентифицированный
  `ctx`, их паники ловит recovery, их исход попадает в access-log.
- `otelgrpc.WithMessageEvents(...)` включает событие спана на каждое сообщение —
  для длинных token-стримов это раздувает спан, поэтому по умолчанию выключено.
- `Server.GRPC()` отдаёт нижележащий `*grpc.Server` — для поздней регистрации
  сервисов или интеропа.

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

`grpckit.Config{}` без полей валиден: дефолты подставляются в `New`.
`srv.Addr()` после `Serve` отдаёт фактический адрес — он нужен, когда порт
выбирает ядро (`GRPC_ADDR=127.0.0.1:0`).

## Reflection toggle

Reflection выключен по умолчанию; включается `GRPC_REFLECTION=true`
(`Config.Reflection`). Удобен для `grpcurl`/отладки в dev. В prod держим
выключенным — он раскрывает список методов и при этом exempt от auth.

## TLS

TLS включается заданием **обоих** `GRPC_TLS_CERT_FILE` и `GRPC_TLS_KEY_FILE`;
одна половина пары — `ErrConfig` на старте. Без пары сервер слушает plaintext
(типично, когда TLS терминируется на mesh/ingress). mTLS и нестандартные
tls-параметры — через `WithServerOptions` (выше). Подробности —
[configuration.md](configuration.md).
