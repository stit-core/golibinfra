# httpkit — рецепты

## API-поверхность (кратко)

- **Сервер**: `NewServer(cfg, schemes...) *Server`; методы `Router() *chi.Mux`,
  `API()/BindAPI() huma.API`, `Start() error`, `Stop(ctx) error`.
- **Middleware** (net/http): `RequestID`, `Recovery(log)`, `AccessLog(log)`,
  `OTel`, `CORS(cfg)`, `RateLimit(cfg)`, `MaxBody(cfg)`, `Metrics(meter)`,
  `SecurityHeaders(cfg)`, `Idempotency(cfg, store)`. huma-уровень:
  `HumaAuth(api, extract)`.
- **Ошибки**: `APIError`, `Error/BadRequest/Unauthorized/Forbidden/NotFound/
  Conflict/Internal`, `MapErr`, `MapSentinel`.
- **Health**: `RegisterHealth(api, probes...)`, тип `Probe`.
- **Прочее**: `RequestIDFrom(ctx)`, `ContextWithRequestID`, `HeaderRequestID`,
  `IsMaxBodyError`, `NewMemoryStore`, `NewConfig`.

## Сервер через fx Module

`httpkit.Module` provide'ит `*Server` и `huma.API`, собирает middleware из
`Config`, регистрирует `/health` + `/ready`, привязывает lifecycle. `Config`
регистрирует caller.

```go
app := fx.New(
    logkit.Module,                 // logkit.Logger
    httpkit.Module,                // *Server + huma.API
    fx.Provide(httpkit.NewConfig), // Config из ENV

    fx.Invoke(registerRoutes),     // см. ниже
)
app.Run()
```

## Добавить роут

Роуты вешаются на `huma.API` (не на chi-router):

```go
type GetUserInput struct {
    ID string `path:"id"`
}
type GetUserOutput struct {
    Body struct {
        ID   string `json:"id"`
        Name string `json:"name"`
    }
}

func registerRoutes(api huma.API, svc *UserService) {
    huma.Get(api, "/users/{id}", func(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
        u, err := svc.Get(ctx, in.ID)
        if err != nil {
            return nil, httpkit.MapSentinel(err, ErrUserNotFound,
                "USER_NOT_FOUND", http.StatusNotFound, "user not found")
        }
        out := &GetUserOutput{}
        out.Body.ID, out.Body.Name = u.ID, u.Name
        return out, nil
    })
}
```

## APIError / MapErr / MapSentinel

Возвращайте `*APIError` напрямую или переводите доменные ошибки на границе
handler'а. huma сериализует `APIError` с его `Status`.

```go
// Прямой APIError
return nil, httpkit.BadRequest("INVALID_EMAIL", "email is malformed")

// Таблица правил
err = httpkit.MapErr(err,
    httpkit.ErrorMapping{
        Match:  func(e error) bool { return errors.Is(e, pgkit.ErrNotFound) },
        Code:   "NOT_FOUND", Status: http.StatusNotFound, Detail: "resource not found",
    },
    httpkit.ErrorMapping{
        Match:  func(e error) bool { return errors.Is(e, ErrConflict) },
        Code:   "CONFLICT", Status: http.StatusConflict, Detail: "already exists",
    },
)

// Sentinel + типизированная ошибка одного домена
err = httpkit.MapSentinel(err, ErrLocked, "LOCKED", http.StatusForbidden, "account locked")
```

`MapErr`/`MapSentinel` на неузнанной ошибке возвращают её как есть — её
подхватит дефолтный huma-обработчик (500). На `nil` возвращают `nil`.

## Идемпотентность

Middleware дедуплицирует write-запросы (`POST`/`PUT`/`PATCH`) по заголовку
`Idempotency-Key`. GET и запросы без ключа проходят насквозь. Первый ответ
буферизуется и сохраняется в `IdempotencyStore`; повторный запрос с тем же
ключом отдаёт закэшированный ответ (`Content-Type: application/json`).

В fx-флоу middleware подключается **автоматически**, если в графе есть
`IdempotencyStore`:

```go
app := fx.New(
    httpkit.Module,
    cachekit.Module, // provide'ит IdempotencyStore поверх Redis
    // ...
)
```

`IdempotencyStore` — интерфейс `Get(ctx, key) (resp, status, found, err)` /
`Set(ctx, key, resp, status)`. Для тестов/одноинстансных сценариев есть
in-memory: `httpkit.NewMemoryStore(ttl)`. Прод-store с TTL/шарингом между
инстансами — `cachekit` (Redis). Прямое подключение мимо `Module`:

```go
s.Router().Use(httpkit.Idempotency(httpkit.IdempotencyConfig{
    Header: "Idempotency-Key", TTL: 24 * time.Hour, MaxBodyBytes: 65536,
}, store))
```

## Security headers

Включены всегда (через `Config.Security`). Пустое поле → заголовок не ставится.
CSP по умолчанию пуст — задайте `HTTP_CSP`, если он нужен:

```bash
HTTP_HSTS="max-age=63072000; includeSubDomains; preload"
HTTP_CSP="default-src 'self'"
```

## CORS / RateLimit

CORS выключен, пока `HTTP_CORS_ORIGINS` пуст. Включение:

```bash
HTTP_CORS_ORIGINS="https://app.example.com,https://admin.example.com"
HTTP_CORS_METHODS="GET,POST,PUT,DELETE"
```

RateLimit (per-IP) активен по умолчанию (`RPS=100`, `Burst=200`); `RPS=0`
выключает:

```bash
HTTP_RATE_LIMIT_RPS=200
HTTP_RATE_LIMIT_BURST=400
```

## Auth

`HumaAuth` проверяет авторизацию только для операций с непустым
`Operation().Security`. Логика извлечения личности — в `AuthExtractor`
(`func(*http.Request) (context.Context, error)`); ошибка → `401`. В fx-флоу
`AuthExtractor` подключается опционально (`optional:"true"`); без него auth —
no-op.

```go
fx.Provide(func(jwt *authnkit.Verifier) httpkit.AuthExtractor {
    return func(r *http.Request) (context.Context, error) {
        claims, err := jwt.Verify(extractBearer(r))
        if err != nil {
            return nil, err
        }
        return contextWithClaims(r.Context(), claims), nil
    }
})
```

Схемы безопасности для OpenAPI регистрируйте через fx-группу:

```go
fx.Provide(fx.Annotate(
    func() httpkit.SecuritySchemeEntry {
        return httpkit.SecuritySchemeEntry{
            Name:   "Bearer",
            Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
        }
    },
    fx.ResultTags(`group:"httpkit.security_scheme"`),
))
```

## Readiness-probes

`Probe` (`func(ctx) error`) через fx-группу `health` подключается к `/ready`:

```go
fx.Provide(fx.Annotate(
    func(db *pgkit.DB) httpkit.Probe { return pgkit.DBProbe(db) },
    fx.ResultTags(`group:"health"`),
))
```

Любая упавшая probe → `/ready` отвечает `503`.

## Прямой запуск без fx

```go
s := httpkit.NewServer(cfg)
s.Router().Use(
    httpkit.RequestID(),
    httpkit.Recovery(log),
    httpkit.AccessLog(log),
)
api := s.BindAPI() // ОБЯЗАТЕЛЬНО после Use
httpkit.RegisterHealth(api)
huma.Get(api, "/ping", pingHandler)
if err := s.Start(); err != nil { /* ... */ }
```

`BindAPI` после `Use` — иначе chi паникует (`humachi.New` регистрирует
`/openapi.json` + `/docs`, и `router.Use` после route'ов запрещён). См.
[architecture.md](architecture.md) § Ленивый BindAPI.
