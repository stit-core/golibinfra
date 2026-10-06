# httpkit — рецепты

## API-поверхность (кратко)

- **Сервер**: `NewServer(cfg, log, opts...) (*Server, error)` — та же сборка, что у `Module`
  (цепочка кита, `HumaAuth` в `BindAPI`; невалидный `Config` — `ErrConfig`; log nil —
  `logkit.Nop()`); опции `WithSecuritySchemes`, `WithAuthExtractor`, `WithIdempotencyStore`,
  `WithMiddleware`, `WithHumaConfig`, `WithPublicRoutes` (с nil — ничего). Методы:
  `Router()`, `API()`/`BindAPI()`, `CheckRoutes()`, `Listen()`, `Addr()`, `Serve()`, `Stop(ctx)`.
- **Middleware** (net/http): `RequestID`, `ClientIP(trustProxyHops, log)`,
  `OTel(quietPaths...)`, `Recovery(log)`,
  `SecurityHeaders(cfg)`, `CORS(cfg)`, `RateLimit(cfg)`, `MaxBody(cfg)`,
  `AccessLog(log, quietPaths...)`, `Metrics()`,
  `Idempotency(cfg, store, log, opts...)`. huma-уровень: `HumaAuth(extract, log)`.
- **fx**: `Module`, `Middlewares`, `HumaConfigs`, `PublicRoutes` (значения графа),
  `ProvideSecurityScheme(ctor, anns...)` → группа `SecuritySchemeGroup`.
- **Ошибки**: `APIError`, `FieldError`, `Error/BadRequest/Unauthorized/Forbidden/NotFound/
  Conflict/Internal/Unavailable`, `ErrorMap` (`NewErrorMap`, `ErrorRule`, `Map`,
  `Declare`, `Unmapped`), `ProblemContentType`.
- **Auth и маршруты**: `AuthExtractor`, `ErrAuthUnavailable`, `ErrAuthNotConfigured`,
  `ContextWithSubject`, `SubjectFrom`, `Public`, `PublicRoutes`, `ErrRoutes`.
- **Health**: `RegisterHealth(api, probeTimeout, log, probes...)`, `Probe`
  (= `healthkit.Probe`); проверка здоровья контейнера — `Healthcheck(ctx)` (код выхода),
  `CheckHealth(ctx, addr)`.
- **Прочее**: `RequestIDFrom`, `ContextWithRequestID`, `HeaderRequestID`,
  `HeaderIdempotentReplayed`, `ClientIPFrom`, `IsMaxBodyError`, `NewMemoryStore`,
  `NewConfig`, `Config.Validate`, `ErrConfig`.

## Сервер через fx Module

```go
app := fx.New(
    logkit.Module,
    fx.Provide(logkit.NewConfig, logkit.NewRedactConfig, httpkit.NewConfig),
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
    huma.Register(api, userErrors.Declare(huma.Operation{
        OperationID: "getUser", Method: http.MethodGet, Path: "/users/{id}",
    }), func(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
        u, err := svc.Get(ctx, in.ID)
        if err != nil {
            return nil, userErrors.Map(err)
        }
        out := &GetUserOutput{}
        out.Body.ID, out.Body.Name = u.ID, u.Name
        return out, nil
    })
}
```

## Ошибки: APIError и ErrorMap

Проверка входа в хендлере — готовый `APIError`:

```go
return nil, httpkit.BadRequest("INVALID_EMAIL", "email is malformed")
```

Доменные ошибки — одна таблица `ErrorMap` на домен или операцию, первое
совпавшее правило выигрывает. Правило ловит sentinel (`Err`, по `errors.Is`) или
предикат (`Match`), `Field` кладёт ошибку поля в `errors[]`:

```go
var userErrors = httpkit.NewErrorMap(
    httpkit.ErrorRule{Err: user.ErrNotFound, Status: http.StatusNotFound, Code: "USER_NOT_FOUND", Detail: "user not found"},
    httpkit.ErrorRule{Err: user.ErrConflict, Status: http.StatusConflict, Code: "USER_CONCURRENT_UPDATE", Detail: "user changed concurrently"},
    httpkit.ErrorRule{Match: policy.IsForbidden, Status: http.StatusForbidden, Code: "FORBIDDEN", Detail: "forbidden"},
    httpkit.ErrorRule{Err: user.ErrWeakPassword, Status: http.StatusUnprocessableEntity,
        Code: "USER_WEAK_PASSWORD", Detail: "password too weak", Field: "body.password"},
    httpkit.ErrorRule{Err: auth.ErrLocked, Status: http.StatusTooManyRequests, Code: "AUTH_LOCKED", Detail: "too many attempts"},
)
```

- `Map(err)` — в хендлере: совпавшая ошибка становится `*APIError`, несовпавшая
  возвращается как есть, и кит отвечает на неё `500 INTERNAL` без текста причины,
  а причину пишет строкой `handler error` с `operation` и тем же `request_id`.
  `nil` → `nil`.
- `Declare(op)` — при регистрации: статусы правил попадают в `op.Errors` (huma
  добавляет к ним 422 и 500), коды — в описание ответа своего статуса в
  `openapi.json` (`Conflict: USER_CONCURRENT_UPDATE`). Операция с `Security`
  объявляет `401` сама (`Unauthorized: UNAUTHORIZED`, коды 401 из `Declare` —
  рядом).
- `Unmapped(errs...)` — в тесте полноты: sentinel'ы домена, которых таблица не
  ловит (ждём пусто).

`NewErrorMap` паникует на сломанной таблице: у правила не ровно одно из `Err` и
`Match`, нет `Code`, статус вне 400–599 или `Err` уже ловит правило выше (частное
ставьте над общим). Когда повторить запрос, ответ говорит заголовком
`Retry-After`: его значение — метод `RetryAfter() time.Duration` доменной ошибки
в цепочке (у 429 — не меньше секунды):

```go
type LockedError struct{ Wait time.Duration }

func (e *LockedError) Error() string              { return "account locked" }
func (e *LockedError) Unwrap() error              { return auth.ErrLocked }
func (e *LockedError) RetryAfter() time.Duration { return e.Wait }
```

Полная форма ответа — [architecture.md § Контракт ошибок](architecture.md#контракт-ошибок).

## Идемпотентность

`POST`/`PUT`/`PATCH` с заголовком `Idempotency-Key` выполняются один раз, повтор
получает сохранённый ответ (`Idempotent-Replayed: true`, воспроизводятся
`Content-Type`, `Location`, `ETag`, `Content-Language`, `Content-Disposition`,
`Cache-Control`; тело ошибки — с `request_id` исходного запроса). Тот же ключ с другим методом, путём, query или телом → `422
IDEMPOTENCY_KEY_REUSED`; параллельный повтор, пока первый выполняется → `409
IDEMPOTENCY_IN_PROGRESS`. Ответы `5xx` не сохраняются, запрос или ответ больше
`MaxBodyBytes` идёт без идемпотентности, ошибка store — строка лога
`idempotency store failed` и fail-open (метрика `result=error`) или `503` при
`HTTP_IDEMPOTENCY_FAIL_CLOSED=true`.

Ключ привязан к клиенту: к субъекту, если extractor его положил, иначе к хешу
`Authorization` + `Cookie`. Когда в графе есть `AuthExtractor`, replay отдаётся
только после успешной аутентификации.

**Без валидных credentials дедупликации нет.** Extractor зовётся на каждом
write-запросе с ключом, независимо от `Operation().Security`; его ошибка даёт
`result=skipped` и сквозной проход. Публичная write-операция (`httpkit.Public`) с
анонимным клиентом выполняется столько раз, сколько раз пришёл запрос, — закрывайте
такие операции `Security` или не опирайтесь на `Idempotency-Key` в их контракте.

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
HTTP_TRUST_PROXY_HOPS=1                   # один доверенный прокси (Traefik/nginx)
```

За reverse-proxy с `HTTP_TRUST_PROXY_HOPS=0` все клиенты приходят с адреса прокси и
делят один bucket (строка Warn подсказывает настройку); с `N` IP клиента — N-я справа
запись `X-Forwarded-For`, которую дописали прокси. Без прокси перед сервисом — только
`0`. Тот же адрес — для аудита и антибрутфорса, свой middleware не нужен:

```go
ip := httpkit.ClientIPFrom(ctx) // ClientIP цепочки кладёт его до OTel
```

## Auth

`HumaAuth` проверяет операции с непустым `Operation().Security`. Когда в графе есть
схема безопасности, открытость операции — явная: `Security` со схемой из
`components.securitySchemes` или пометка `httpkit.Public`; операция без того и
другого (в том числе скрытая и из группы huma) — ошибка старта, пустой `Security`
публичным не считается:

```go
huma.Register(api, huma.Operation{
    OperationID: "get-me", Method: http.MethodGet, Path: "/me",
    Security: []map[string][]string{{"Bearer": {}}},   // закрыто
}, handler)

huma.Register(api, httpkit.Public(huma.Operation{  // открыто без аутентификации
    OperationID: "get-status", Method: http.MethodGet, Path: "/status",
}), handler)
```

`Public` совместим с `ErrorMap.Declare` (`httpkit.Public(errs.Declare(op))`). Маршрут
мимо `huma.Register` — `Router().Get`, `Mount`, `api.Adapter().Handle` — `HumaAuth` не
видит, поэтому при схеме в графе он тоже ошибка старта, пока его путь не объявлен
открытым одним значением на граф:

```go
fx.Supply(httpkit.PublicRoutes{"/.well-known/jwks.json"}) // путь chi; без fx — WithPublicRoutes
```

На METHOD+path операции такой маршрут — ошибка старта всегда, в любом порядке и при
`PublicRoutes`: chi оставляет маршруту последний хендлер. Маршруты регистрируют в Invoke или
конструкторе: добавленный позже (в `OnStart` после хука сервера, из хендлера) проверку не
проходит.

`AuthExtractor` (`func(*http.Request) (context.Context, error)`) кладёт личность
в ctx; ошибка → `401 UNAUTHORIZED`, её текст — только в лог. Инфраструктурную
ошибку (JWKS/хаб недоступны) оборачивайте в `httpkit.ErrAuthUnavailable` — клиент
получит `503 AUTH_UNAVAILABLE` вместо разлогина; свой статус задаётся возвратом
`*httpkit.APIError`. Операция с `Security` без extractor'а в графе, в том числе
скрытая, не даёт стартовать (`httpkit.ErrAuthNotConfigured` с именами операций).

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

httpkit.ProvideSecurityScheme(func() httpkit.SecuritySchemeEntry {
    return httpkit.SecuritySchemeEntry{Name: "Bearer",
        Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"}}
})
```

`ProvideSecurityScheme` принимает конструктор `SecuritySchemeEntry` или
`[]SecuritySchemeEntry` (несколько схем одним конструктором); другой тип результата —
ошибка сборки графа.

Имя схемы в графе одно: дубль имени и схема без `Scheme` — ошибка сборки графа
(`httpkit.ErrConfig` с обоими значениями).

## Свои middleware и настройка huma

Middleware потребителя — одно значение `httpkit.Middlewares` на граф, в порядке
слайса (первый — внешний), правки `huma.Config` — одно значение
`httpkit.HumaConfigs`; их провайдит приложение, поэтому порядок зависимых правок
явный:

```go
fx.Provide(func(t *tenantMiddleware, a *auditMiddleware) httpkit.Middlewares {
    return httpkit.Middlewares{t.Handle, a.Handle} // аудит видит tenant
})

fx.Supply(httpkit.HumaConfigs{ // правки huma.Config — в порядке слайса, после кита
    func(c *huma.Config) { c.Servers = []*huma.Server{{URL: "https://api.example.com"}} },
})
```

## Readiness-probes

```go
healthkit.ProvideProbe(pgkit.Probe)
natskit.ProvideProbe("")
healthkit.ProvideProbe(cachekit.Probe)
healthkit.ProvideProbe(func(s *Sync) healthkit.Probe { return healthkit.New("sync", s.Ready) })
```

Пробы выполняются параллельно, каждая — не дольше `HTTP_READY_PROBE_TIMEOUT`.
Любая упавшая → `503` с телом
`{"status":"unhealthy","probes":[{"name":"pgkit","status":"down"}]}`; текст
ошибки — в логе. panic пробы — тоже `down` и ERROR `readiness probe panicked` со
стеком, процесс жив. С начала остановки `/ready` отвечает `503
{"status":"draining"}`.

## Остановка за балансировщиком

Под балансировщиком (k8s Service, ingress, Traefik) задайте паузу дренажа
`HTTP_SHUTDOWN_DRAIN_DELAY`, обычно 5–10 с: пока она идёт, listener принимает
запросы, а отправители узнают об остановке. В k8s удаляемый под сразу выпадает из
EndpointSlice — пауза покрывает время, пока это дойдёт до kube-proxy и ingress.
Балансировщик со своей проверкой здоровья (Traefik, ALB) выводит инстанс по `503`
`/ready` — пауза не меньше его интервала проверки × порога отказов. Без паузы
(`0s`, по умолчанию) `/ready` 503 на остановке почти не виден: listener закрывается
сразу.

Пауза, `HTTP_SHUTDOWN_TIMEOUT` и бюджеты остальных хуков (`NATS_DRAIN_TIMEOUT`, …)
укладываются в `fx.StopTimeout`, а он — в `terminationGracePeriodSeconds`; HTTP берёт
не больше двух третей бюджета ([configuration.md § Остановка](configuration.md#остановка)):

```go
fx.New(
    fx.StopTimeout(30*time.Second), // HTTP_SHUTDOWN_DRAIN_DELAY=5s + HTTP_SHUTDOWN_TIMEOUT=10s ≤ 20s
    // …
)
```

## Проверка здоровья контейнера

В distroless-образе нет shell и curl: проверку здоровья делает сам бинарь
подкомандой. `Healthcheck` спрашивает `/health` адреса `HTTP_ADDR` и отдаёт код
выхода — `0`, если сервис ответил `200` не дольше 3 секунд, иначе `1` и ERROR
`healthcheck failed`. Запрос идёт напрямую, мимо `HTTP_PROXY` окружения; хост «все
интерфейсы» (`:8080`, `0.0.0.0`, `[::]`) — `127.0.0.1`:

```go
func main() {
    if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
        os.Exit(httpkit.Healthcheck(context.Background()))
    }
    fx.New(service).Run()
}
```

```yaml
healthcheck:
  test: ["CMD", "/app/api", "healthcheck"]
```

`CheckHealth(ctx, addr)` — та же проверка одного адреса, например в boot-тесте:
`httpkit.CheckHealth(t.Context(), srv.Addr().String())`.

## SSE и WebSocket

Wrapper ответа реализует `http.Flusher`, `Unwrap()` и `http.Hijacker`, поэтому
стриминг и upgrade работают через всю цепочку `Module`:

```go
func stream(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/event-stream")
    rc := http.NewResponseController(w)
    for {
        select {
        case <-r.Context().Done(): // клиент ушёл или дедлайн остановки сервера
            return
        case ev := <-events:
            fmt.Fprintf(w, "data: %s\n\n", ev)
            _ = rc.Flush()
        }
    }
}
```

Для долгих стримов задайте `HTTP_WRITE_TIMEOUT=0` (или продлевайте дедлайн через
`ResponseController.SetWriteDeadline`). На остановке поток держит соединение до
дедлайна `HTTP_SHUTDOWN_TIMEOUT`, затем ctx запроса отменяется — и у WebSocket на
захваченном соединении: обработчик, который слушает `r.Context()`, завершается
сам.

## Прямой запуск без fx

```go
s, err := httpkit.NewServer(cfg, log, httpkit.WithAuthExtractor(extract)) // цепочка кита, как у Module
if err != nil { /* невалидный Config или схема — ErrConfig */ }
api := s.BindAPI() // HumaAuth — первым huma-middleware: Security проверяется extractor'ом
httpkit.RegisterHealth(api, cfg.ReadyProbeTimeout, log)
huma.Register(api, httpkit.Public(huma.Operation{Method: http.MethodGet, Path: "/ping"}), pingHandler)
if err := s.CheckRoutes(); err != nil { /* операции и маршруты — как на старте Module */ }
if err := s.Listen(); err != nil { /* порт занят */ }
go func() { _ = s.Serve() }()
// ...
_ = s.Stop(ctx) // /ready 503, пауза дренажа, дедлайн: ctx хендлеров отменён, соединения закрыты
```
