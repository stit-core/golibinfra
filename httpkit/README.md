# httpkit

HTTP-сервер на [huma/v2](https://github.com/danielgtaylor/huma) поверх
[chi](https://github.com/go-chi/chi) с готовой middleware-цепочкой и единой
error-моделью. Один `Server` несёт `*chi.Mux` (транспорт) и `huma.API`
(типобезопасные операции + OpenAPI). Из коробки: `RequestID`, `OTel`,
`AccessLog`, `Metrics`, `Recovery`, `SecurityHeaders`, `CORS`, `RateLimit`,
`MaxBody`, опц. `Idempotency`, `HumaAuth`; ошибки — `APIError`
(`{code,status,title,detail}`, `application/problem+json`), незамапленная ошибка
handler'а уходит в лог, а клиенту — нейтральный 500. `/health` и `/ready`
(по пробам из fx-группы `healthkit.probes`) регистрируются автоматически. RED-метрики и
server-span — by default.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/httpkit"
```

## Быстрый старт

`Module` provide'ит `*Server` и `huma.API`, собирает цепочку из `Config`,
регистрирует `/health` + `/ready` и привязывает lifecycle: порт занимается
синхронно в `OnStart` (занятый адрес — ошибка старта), graceful shutdown в
`OnStop`, падение `Serve` гасит приложение. `Config` регистрирует caller.

```go
app := fx.New(
    logkit.Module,                                  // logkit.Logger
    fx.Provide(logkit.NewConfig, httpkit.NewConfig), // LOG_*/HTTP_* из ENV + Validate
    httpkit.Module,                                 // *Server + huma.API
    healthkit.Provide(pgkit.Probe),               // проба в /ready
    fx.Invoke(func(api huma.API) {
        huma.Get(api, "/users/{id}", func(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
            u, err := svc.Get(ctx, in.ID)
            if err != nil {
                return nil, httpkit.MapIs(err, ErrUserNotFound, "USER_NOT_FOUND", http.StatusNotFound, "user not found")
            }
            // ...
        })
    }),
)
app.Run()
```

Без fx (caller сам ставит middleware и вызывает `BindAPI` после `Use`):

```go
s := httpkit.NewServer(cfg, httpkit.WithLogger(log))
s.Router().Use(httpkit.RequestID(), httpkit.OTel(), httpkit.AccessLog(log), httpkit.Metrics(), httpkit.Recovery(log))
api := s.BindAPI()
httpkit.RegisterHealth(api, log, cfg.ReadyProbeTimeout, pgkit.Probe(db))
huma.Get(api, "/ping", handler)
_ = s.Serve()
```

## Документация

- [docs/architecture.md](docs/architecture.md) — huma + chi, ленивый `BindAPI`,
  точки расширения, порядок middleware и почему он такой, wrapper ответа
  (`Flush`/`Hijack`), error-модель, auth, идемпотентность, health.
- [docs/configuration.md](docs/configuration.md) — справочник ENV: `Config`,
  `CORSConfig`, `RateLimitConfig`, `MaxBodyConfig`, `SecurityHeadersConfig`,
  `IdempotencyConfig`, правила `Validate`.
- [docs/observability.md](docs/observability.md) — метрики, labels, спаны, логи.
- [docs/usage.md](docs/usage.md) — рецепты: роуты, `MapIs`/`MapAs`,
  идемпотентность, CORS/RateLimit за прокси, auth, свои middleware и `huma.Config`,
  readiness, SSE/WebSocket.
- [docs/testing.md](docs/testing.md) — `testkit/handlertest`, тесты middleware,
  метрик и fx-проводки.
