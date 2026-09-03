# httpkit — рецепты

## API-поверхность (кратко)

- **Сервер**: `NewServer(cfg, schemes...) *Server`; `Router()`, `API()/BindAPI()`,
  `Listen()`, `Addr()`, `Serve()`/`Start()`, `Stop(ctx)`.
- **Middleware** (net/http): `RequestID`, `OTel`, `Recovery(log)`,
  `SecurityHeaders(cfg)`, `CORS(cfg)`, `RateLimit(cfg)`, `MaxBody(cfg)`,
  `AccessLog(log)`, `Metrics()`, `Idempotency(cfg, store)`. huma-уровень:
  `HumaAuth(api, extract)`.
- **Ошибки**: `APIError`, `Error/BadRequest/Unauthorized/Forbidden/NotFound/
  Conflict/Internal`, `MapErr`, `MapIs`, `MapAs[T]`, `ProblemContentType`.
- **Health**: `RegisterHealth(api, log, probes...)`, `Probe` (= `healthkit.Probe`).
- **Прочее**: `RequestIDFrom`, `ContextWithRequestID`, `HeaderRequestID`,
  `IsMaxBodyError`, `NewMemoryStore`, `NewConfig`, `Config.Validate`, `ErrConfig`.

## Сервер через fx Module

```go
app := fx.New(
    logkit.Module,
    fx.Provide(httpkit.NewConfig),
    httpkit.Module,
    fx.Invoke(registerRoutes),
)
app.Run()
```

## Добавить роут

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
            return nil, httpkit.MapIs(err, ErrUserNotFound, "USER_NOT_FOUND", http.StatusNotFound, "user not found")
        }
        out := &GetUserOutput{}
        out.Body.ID, out.Body.Name = u.ID, u.Name
        return out, nil
    })
}
```

## APIError / MapErr / MapIs / MapAs

```go
return nil, httpkit.BadRequest("INVALID_EMAIL", "email is malformed")

err = httpkit.MapErr(err,
    httpkit.ErrorMapping{Match: func(e error) bool { return errors.Is(e, pgkit.ErrNotFound) },
        Code: "NOT_FOUND", Status: http.StatusNotFound, Detail: "resource not found"},
    httpkit.ErrorMapping{Match: func(e error) bool { return errors.Is(e, ErrConflict) },
        Code: "CONFLICT", Status: http.StatusConflict, Detail: "already exists"},
)

err = httpkit.MapIs(err, ErrLocked, "LOCKED", http.StatusForbidden, "account locked")
err = httpkit.MapAs[*ValidationError](err, "VALIDATION", http.StatusUnprocessableEntity, "invalid input")
```

Неузнанная ошибка возвращается как есть (500 от huma); `nil` → `nil`.
`MapAs[error]` паникует: такой маппинг превратил бы любую ошибку, включая
упавшую БД, в выбранный статус.

## Идемпотентность

`POST`/`PUT`/`PATCH` с заголовком `Idempotency-Key` выполняются один раз, повтор
получает сохранённый ответ (`Idempotent-Replayed: true`, воспроизводятся
`Content-Type`, `Location`, `ETag`, `Content-Language`, `Content-Disposition`,
`Cache-Control`). Ключ привязан к клиенту (`Authorization` + `Cookie`): чужой
ключ не отдаёт чужой ответ. Тот же ключ с другим методом/путём/телом → `422
IDEMPOTENCY_KEY_REUSED`; параллельный повтор, пока первый выполняется → `409
IDEMPOTENCY_IN_PROGRESS`. Ответы `5xx` не сохраняются, запрос или ответ больше
`MaxBodyBytes` идёт без идемпотентности, ошибка store — fail-open (запрос
выполняется, метрика `result=error`).

Подключение через fx: положите в граф `IdempotencyStore` — `Module` навесит
middleware с `Config.Idempotency`:

```go
fx.Provide(func(c cachekit.Cache) httpkit.IdempotencyStore { return cachekit.NewIdempotencyStore(c) })
```

Контракт store — байтовый, запись сериализует httpkit:

```go
type IdempotencyStore interface {
    Reserve(ctx context.Context, key string, ttl time.Duration) (bool, error)
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Set(ctx context.Context, key string, record []byte, ttl time.Duration) error
    Release(ctx context.Context, key string) error
}
```

`httpkit.NewMemoryStore()` — in-memory store для тестов и одного процесса.

## Security headers

Всегда включены; пустое поле — заголовок не ставится. CSP по умолчанию пуст:

```bash
HTTP_HSTS="max-age=63072000; includeSubDomains; preload"
HTTP_CSP="default-src 'self'"
```

## CORS / RateLimit

```bash
HTTP_CORS_ORIGINS="https://app.example.com,https://admin.example.com"
HTTP_CORS_ALLOW_CREDENTIALS=true          # cookie-сессии; несовместимо с "*"

HTTP_RATE_LIMIT_RPS=200
HTTP_RATE_LIMIT_BURST=400
HTTP_RATE_LIMIT_TRUST_PROXY=true          # только за своим nginx/traefik
```

За reverse-proxy без `TrustProxy` все клиенты приходят с одного IP и делят
один bucket; с `TrustProxy` без прокси заголовок `X-Forwarded-For`
подделывается. Выбирайте по топологии.

## Auth

`HumaAuth` проверяет только операции с непустым `Operation().Security`.
`AuthExtractor` (`func(*http.Request) (context.Context, error)`) кладёт личность
в ctx; ошибка → `401`. Без extractor'а в графе auth — no-op.

```go
fx.Provide(func(v *authnkit.Verifier) httpkit.AuthExtractor {
    return func(r *http.Request) (context.Context, error) {
        claims, err := v.Verify(extractBearer(r))
        if err != nil {
            return nil, err
        }
        return contextWithClaims(r.Context(), claims), nil
    }
})

fx.Provide(fx.Annotate(
    func() httpkit.SecuritySchemeEntry {
        return httpkit.SecuritySchemeEntry{Name: "Bearer",
            Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"}}
    },
    fx.ResultTags(`group:"httpkit.security_scheme"`),
))
```

## Readiness-probes

```go
healthkit.Provide(pgkit.DBProbe)
healthkit.Provide(natskit.Probe)
healthkit.Provide(cachekit.Probe)
healthkit.Provide(func(s *Sync) healthkit.Probe { return s.Ready })
```

Любая упавшая проба → `/ready` отвечает `503 unhealthy`; текст ошибки — в логе.

## SSE и WebSocket

Wrapper ответа реализует `http.Flusher`, `Unwrap()` и `http.Hijacker`, поэтому
стриминг и upgrade работают через всю цепочку `Module`:

```go
func stream(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/event-stream")
    rc := http.NewResponseController(w)
    for ev := range events {
        fmt.Fprintf(w, "data: %s\n\n", ev)
        _ = rc.Flush()
    }
}
```

Для долгих стримов задайте `HTTP_WRITE_TIMEOUT=0` (или продлевайте дедлайн через
`ResponseController.SetWriteDeadline`).

## Прямой запуск без fx

```go
s := httpkit.NewServer(cfg)
s.Router().Use(httpkit.RequestID(), httpkit.OTel(), httpkit.Recovery(log), httpkit.AccessLog(log), httpkit.Metrics())
api := s.BindAPI() // после Use
httpkit.RegisterHealth(api, log)
huma.Get(api, "/ping", pingHandler)
if err := s.Listen(); err != nil { /* порт занят */ }
go func() { _ = s.Serve() }()
// ...
_ = s.Stop(ctx)
```
