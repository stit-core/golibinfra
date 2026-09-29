# httpkit — рецепты

## API-поверхность (кратко)

- **Сервер**: `NewServer(cfg, opts...) *Server`; опции `WithLogger`,
  `WithSecuritySchemes`, `WithMiddleware`, `WithHumaConfig`. Методы: `Router()`,
  `API()`/`BindAPI()`, `Listen()`, `Addr()`, `Serve()`, `Stop(ctx)`.
- **Middleware** (net/http): `RequestID`, `OTel(quietPaths...)`, `Recovery(log)`,
  `SecurityHeaders(cfg)`, `CORS(cfg)`, `RateLimit(cfg)`, `MaxBody(cfg)`,
  `AccessLog(log, quietPaths...)`, `Metrics()`,
  `Idempotency(cfg, store, opts...)`. huma-уровень: `HumaAuth(log, extract)`.
- **Ошибки**: `APIError`, `Error/BadRequest/Unauthorized/Forbidden/NotFound/
  Conflict/Internal/Unavailable`, `MapErr`, `MapIs`, `MapAs[T]`,
  `ProblemContentType`.
- **Auth**: `AuthExtractor`, `ErrAuthUnavailable`, `ErrAuthNotConfigured`,
  `ContextWithSubject`, `SubjectFrom`.
- **Health**: `RegisterHealth(api, log, probeTimeout, probes...)`, `Probe`
  (= `healthkit.Probe`).
- **Прочее**: `RequestIDFrom`, `ContextWithRequestID`, `HeaderRequestID`,
  `HeaderIdempotentReplayed`, `ClientIP`, `IsMaxBodyError`, `NewMemoryStore`,
  `NewConfig`, `Config.Validate`, `ErrConfig`.

## Сервер через fx Module

```go
app := fx.New(
    logkit.Module,
    fx.Provide(logkit.NewConfig, httpkit.NewConfig),
    fx.Invoke(registerRoutes), // корневой Invoke с зависимостями — до httpkit.Module
    httpkit.Module,
)
app.Run()
```

HTTP стартует последним и гасится первым: `Module` — на верхнем уровне `fx.New`,
роуты регистрирует Invoke внутри `fx.Module` домена или корневой `fx.Invoke` до
`Module` ([architecture.md § Порядок хуков](architecture.md#порядок-хуков)).

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

Незамапленная ошибка клиенту не показывается: он получает `500
{"code":"INTERNAL","detail":"internal error"}`, а полный текст уходит в лог
строкой `handler error` с `request_id`. `nil` → `nil`. `MapAs[error]` паникует:
такой маппинг превратил бы любую ошибку, включая упавшую БД, в выбранный статус.

## Идемпотентность

`POST`/`PUT`/`PATCH` с заголовком `Idempotency-Key` выполняются один раз, повтор
получает сохранённый ответ (`Idempotent-Replayed: true`, воспроизводятся
`Content-Type`, `Location`, `ETag`, `Content-Language`, `Content-Disposition`,
`Cache-Control`). Тот же ключ с другим методом, путём, query или телом → `422
IDEMPOTENCY_KEY_REUSED`; параллельный повтор, пока первый выполняется → `409
IDEMPOTENCY_IN_PROGRESS`. Ответы `5xx` не сохраняются, запрос или ответ больше
`MaxBodyBytes` идёт без идемпотентности, ошибка store — fail-open (метрика
`result=error`) или `503` при `HTTP_IDEMPOTENCY_FAIL_CLOSED=true`.

Ключ привязан к клиенту: к субъекту, если extractor его положил, иначе к хешу
`Authorization` + `Cookie`. Когда в графе есть `AuthExtractor`, replay отдаётся
только после успешной аутентификации.

**Без валидных credentials дедупликации нет.** Extractor зовётся на каждом
write-запросе с ключом, независимо от `Operation().Security`; его ошибка даёт
`result=skipped` и сквозной проход. Публичная write-операция
(`Security: []map[string][]string{}`) с анонимным клиентом выполняется столько
раз, сколько раз пришёл запрос, — закрывайте такие операции `Security` или не
опирайтесь на `Idempotency-Key` в их контракте.

```go
fx.Provide(func(v *oidckit.Verifier) httpkit.AuthExtractor {
    return func(r *http.Request) (context.Context, error) {
        id, err := v.Verify(r)
        if err != nil {
            return nil, err
        }
        // Субъект — стабильный scope идемпотентности: ротация токена не
        // превращает ретрай в новую операцию.
        return httpkit.ContextWithSubject(contextWithIdentity(r.Context(), id), id.Subject), nil
    }
})
```

Подключение store через fx — `Module` навесит middleware с `Config.Idempotency`:

```go
fx.Provide(func(c cachekit.Cache) httpkit.IdempotencyStore { return cachekit.NewIdempotencyStore(c) })
```

Контракт store — байтовый, запись сериализует httpkit. `Reserve` возвращает
токен владельца резерва, `Release` снимает резерв, **только** пока ключ держит
этот токен, и не трогает сохранённый ответ:

```go
type IdempotencyStore interface {
    Reserve(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Set(ctx context.Context, key string, record []byte, ttl time.Duration) error
    Release(ctx context.Context, key, token string) error
}
```

Токен обязан быть уникальным на каждый успешный `Reserve`: запрос, переживший
`ReserveTTL`, своим опоздавшим `Release` не должен снимать чужой резерв.

`httpkit.NewMemoryStore()` — in-memory store для тестов и одного процесса
(истёкшие записи выметаются).

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
HTTP_CORS_EXPOSE_HEADERS="X-Request-ID,Location,Retry-After,Idempotent-Replayed"

HTTP_RATE_LIMIT_RPS=200
HTTP_RATE_LIMIT_BURST=400
HTTP_RATE_LIMIT_TRUST_PROXY=true          # ровно один доверенный nginx/traefik
```

За reverse-proxy без `TrustProxy` все клиенты приходят с одного IP и делят
один bucket; с `TrustProxy` IP берётся из правого значения `X-Forwarded-For` —
того, что дописал прокси. Без прокси перед сервисом включать нельзя. Тот же
адрес для аудита и антибрутфорса:

```go
ip := httpkit.ClientIP(r, cfg.RateLimit.TrustProxy)
```

## Auth

`HumaAuth` проверяет только операции с непустым `Operation().Security`, поэтому
непубличные операции обязаны объявлять `Security` явно, а публичные — пустой
`Security: []map[string][]string{}`:

```go
huma.Register(api, huma.Operation{
    OperationID: "get-me", Method: http.MethodGet, Path: "/me",
    Security: []map[string][]string{{"Bearer": {}}},   // закрыто
}, handler)

huma.Register(api, huma.Operation{
    OperationID: "get-status", Method: http.MethodGet, Path: "/status",
    Security: []map[string][]string{},                 // публично, осознанно
}, handler)
```

`AuthExtractor` (`func(*http.Request) (context.Context, error)`) кладёт личность
в ctx; ошибка → `401 UNAUTHORIZED`, её текст — только в лог. Инфраструктурную
ошибку (JWKS/хаб недоступны) оборачивайте в `httpkit.ErrAuthUnavailable` — клиент
получит `503 AUTH_UNAVAILABLE` вместо разлогина; свой статус задаётся возвратом
`*httpkit.APIError`. Операция с `Security` без extractor'а в графе не даёт
стартовать (`httpkit.ErrAuthNotConfigured` с именами операций), а скрытая —
отвечает `500 AUTH_NOT_CONFIGURED`.

```go
fx.Provide(func(v *authnkit.Verifier) httpkit.AuthExtractor {
    return func(r *http.Request) (context.Context, error) {
        claims, err := v.Verify(extractBearer(r))
        if errors.Is(err, authnkit.ErrKeysUnavailable) {
            return nil, fmt.Errorf("verify: %w", httpkit.ErrAuthUnavailable)
        }
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

## Свои middleware и настройка huma

```go
fx.Provide(fx.Annotate(
    func(m *tenantMiddleware) func(http.Handler) http.Handler { return m.Handle },
    fx.ResultTags(`group:"httpkit.middleware"`),
))

fx.Provide(fx.Annotate(
    func() func(*huma.Config) {
        return func(c *huma.Config) { c.Servers = []*huma.Server{{URL: "https://api.example.com"}} }
    },
    fx.ResultTags(`group:"httpkit.huma_config"`),
))
```

## Readiness-probes

```go
healthkit.Provide(pgkit.Probe)
healthkit.Provide(natskit.Probe)
healthkit.Provide(cachekit.Probe)
healthkit.Provide(func(s *Sync) healthkit.Probe { return healthkit.New("sync", s.Ready) })
```

Пробы выполняются параллельно, каждая — не дольше `HTTP_READY_PROBE_TIMEOUT`.
Любая упавшая → `503` с телом
`{"status":"unhealthy","probes":[{"name":"pgkit","status":"down"}]}`; текст
ошибки — в логе.

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
s := httpkit.NewServer(cfg, httpkit.WithLogger(log))
s.Router().Use(httpkit.RequestID(), httpkit.OTel(cfg.QuietPaths...),
    httpkit.AccessLog(log, cfg.QuietPaths...), httpkit.Metrics(), httpkit.Recovery(log))
api := s.BindAPI() // после Use
httpkit.RegisterHealth(api, log, cfg.ReadyProbeTimeout)
huma.Get(api, "/ping", pingHandler)
if err := s.Listen(); err != nil { /* порт занят */ }
go func() { _ = s.Serve() }()
// ...
_ = s.Stop(ctx)
```
