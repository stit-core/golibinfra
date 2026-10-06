# httpkit

HTTP-сервер на [huma/v2](https://github.com/danielgtaylor/huma) поверх
[chi](https://github.com/go-chi/chi) с готовой middleware-цепочкой и единым
контрактом ошибок. Один `Server` несёт `*chi.Mux` (транспорт) и `huma.API`
(типобезопасные операции + OpenAPI). Из коробки: `RequestID`, `ClientIP` (IP клиента в ctx —
`ClientIPFrom`, доверие прокси — `HTTP_TRUST_PROXY_HOPS`), `OTel`,
`AccessLog`, `Metrics`, `Recovery`, `SecurityHeaders`, `CORS`, `RateLimit`,
`MaxBody` (поднятый лимит операции поднимает и его), дедлайн запроса
(`HTTP_REQUEST_TIMEOUT`), HEAD на GET-маршрутах, опц. `Idempotency`, `HumaAuth`. Любой
ответ-ошибка — `APIError` (`application/problem+json`: `title`, `status`, `detail`,
машинный `code`, `request_id`, `errors[]` полей): хендлер, валидация, auth, неизвестный
маршрут и метод, лимиты, дедлайн, panic; 5xx — без текста причины (она в логе с тем же
`request_id`), 429 — с `Retry-After`, 401 — с `WWW-Authenticate`; panic после начала
ответа обрывает соединение. При схеме безопасности в графе открытость
операции явная — `Security` или `httpkit.Public`; дубли маршрутов (и маршрут мимо huma на
пути операции), забытая пометка и маршрут мимо huma — ошибка старта (`CheckRoutes`). `/health` и `/ready` (по пробам из
fx-группы `healthkit.probes`; panic пробы — проба `down`) регистрируются автоматически.
Остановка: `/ready` отвечает 503 на паузе дренажа (`HTTP_SHUTDOWN_DRAIN_DELAY`), запросы
дорабатывают `HTTP_SHUTDOWN_TIMEOUT`, на дедлайне их ctx отменяется, а соединения
закрываются — хукам после HTTP остаётся треть бюджета `fx.StopTimeout`. Подкоманда
проверки здоровья для образа без shell — `Healthcheck`. RED-метрики и server-span — by
default.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/httpkit"
```

## Быстрый старт

`Module` provide'ит `*Server` и `huma.API`, собирает цепочку из `Config`,
регистрирует `/health` + `/ready` и привязывает lifecycle: порт занимается
синхронно в `OnStart` (занятый адрес — ошибка старта), graceful shutdown в
`OnStop`, падение `Serve` гасит приложение. HTTP начинает слушать после `OnStart`
всех `fx.Module` графа и гасится до их `OnStop`, где бы `Module` ни стоял среди
модулей; условие — `Module` на верхнем уровне `fx.New`, а корневой `fx.Invoke`,
который строит зависимости (в том числе регистрирует роуты с хендлерами), — до
него или внутри `fx.Module`. `Config` регистрирует caller.

```go
var userErrors = httpkit.NewErrorMap(
    httpkit.ErrorRule{Err: ErrUserNotFound, Status: http.StatusNotFound, Code: "USER_NOT_FOUND", Detail: "user not found"},
)

app := fx.New(
    logkit.Module,                                  // logkit.Logger
    fx.Provide(logkit.NewConfig, logkit.NewRedactConfig, httpkit.NewConfig), // LOG_*/HTTP_* из ENV
    healthkit.ProvideProbe(pgkit.Probe),                 // проба в /ready
    fx.Invoke(func(api huma.API) {                  // корневой Invoke — до httpkit.Module
        huma.Register(api, userErrors.Declare(huma.Operation{ // коды — в openapi.json
            OperationID: "getUser", Method: http.MethodGet, Path: "/users/{id}",
        }), func(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
            u, err := svc.Get(ctx, in.ID)
            if err != nil {
                return nil, userErrors.Map(err) // несопоставленная → 500 без причины + лог
            }
            // ...
        })
    }),
    httpkit.Module,                                 // *Server + huma.API; HTTP стартует последним
)
app.Run()
```

Без fx — `NewServer`, ту же сборку зовёт `Module`: цепочка middleware кита из `Config`,
`HumaAuth` в `BindAPI` (extractor — `WithAuthExtractor`), невалидный `Config` — `ErrConfig`;
проверку старта `Module` делает `s.CheckRoutes()`:

```go
s, err := httpkit.NewServer(cfg, log, httpkit.WithAuthExtractor(extract))
if err != nil {
    return err
}
api := s.BindAPI()
httpkit.RegisterHealth(api, cfg.ReadyProbeTimeout, log, pgkit.Probe(db))
huma.Get(api, "/ping", handler)
if err := s.CheckRoutes(); err != nil {
    return err
}
_ = s.Serve()
```

## Документация

- [docs/architecture.md](docs/architecture.md) — huma + chi, ленивый `BindAPI`,
  точки расширения, порядок middleware и почему он такой, wrapper ответа
  (`Flush`/`Hijack`), контракт ошибок, auth, идемпотентность, health.
- [docs/configuration.md](docs/configuration.md) — справочник ENV: `Config`,
  `CORSConfig`, `RateLimitConfig`, `MaxBodyConfig`, `SecurityHeadersConfig`,
  `IdempotencyConfig`, правила `Validate`.
- [docs/observability.md](docs/observability.md) — метрики, labels, спаны, логи.
- [docs/usage.md](docs/usage.md) — рецепты: роуты, `ErrorMap`,
  идемпотентность, CORS/RateLimit за прокси, auth, свои middleware и `huma.Config`,
  readiness, SSE/WebSocket.
- [docs/testing.md](docs/testing.md) — `testkit/handlertest`, тесты middleware,
  метрик и fx-проводки.
