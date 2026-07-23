# httpkit

HTTP-сервер на [huma/v2](https://github.com/danielgtaylor/huma) поверх
[chi](https://github.com/go-chi/chi) с готовой middleware-цепочкой и
унифицированной error-моделью. Один `Server` несёт `*chi.Mux` (транспорт) и
`huma.API` (типобезопасные операции + OpenAPI). Из коробки: `RequestID`,
`Recovery`, `AccessLog`, `OTel`, `CORS`, `RateLimit`, `MaxBody`, `Metrics`,
`HumaAuth`, `SecurityHeaders`, опц. `Idempotency`; ошибки сериализуются как
`APIError` (`{code,status,title,detail}`). `/health` и `/ready` (с
readiness-probes) регистрируются автоматически. Observability — by default:
RED-метрики и server-span через `otelhttp`.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/httpkit"
```

Версия и `go get` — см. [корневой README](../README.md) § Использование в проекте.

## Быстрый старт

Через fx: `Module` provide'ит `*Server` и `huma.API`, собирает middleware-цепочку
из `Config`, регистрирует `/health` + `/ready` и привязывает lifecycle (старт в
goroutine на `OnStart`, graceful shutdown на `OnStop`). `Config` регистрирует
caller — `Module` его **не** provide'ит (та же asymmetry, что в `pgkit`).

```go
app := fx.New(
    logkit.Module,                    // logkit.Logger
    httpkit.Module,                   // *Server + huma.API
    fx.Provide(httpkit.NewConfig),    // Config из ENV (envconfig)
    fx.Invoke(func(api huma.API) {
        huma.Get(api, "/users/{id}", func(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
            // ...
        })
    }),
)
app.Run()
```

Прямой `New` без fx (caller сам ставит middleware и вызывает `BindAPI` ПОСЛЕ
`router.Use`):

```go
s := httpkit.NewServer(cfg)
s.Router().Use(httpkit.RequestID(), httpkit.Recovery(log))
api := s.BindAPI() // регистрирует /openapi.json и /docs — после Use
huma.Get(api, "/ping", handler)
_ = s.Start()
```

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство (huma + chi,
  ленивый `BindAPI`), конвейер middleware и порядок, error-маппинг, ключевые
  решения-WHY.
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV:
  `Config`, `CORSConfig`, `RateLimitConfig`, `MaxBodyConfig`,
  `SecurityHeadersConfig`, `IdempotencyConfig` (public API).
- [docs/observability.md](docs/observability.md) — единый источник правды по
  метрикам, labels и спанам.
- [docs/usage.md](docs/usage.md) — рецепты: сервер через fx, добавление роута,
  `APIError`/`MapErr`/`MapSentinel`, идемпотентность, security-headers,
  CORS/RateLimit, auth.
- [docs/testing.md](docs/testing.md) — httptest-хелперы из
  `testkit/handlertest` и тестирование middleware.
