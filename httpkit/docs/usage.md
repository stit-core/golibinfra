# httpkit — рецепты

## API-поверхность (кратко)

- **Сервер**: `NewServer(cfg, log, opts...) (*Server, error)` — та же сборка, что у `Module`
  (цепочка кита, `HumaAuth` в `BindAPI`; невалидный `Config` — `ErrConfig`; log nil —
  `logkit.Nop()`); опции `WithSecuritySchemes`, `WithIdempotencyStore`,
  `WithMiddleware`, `WithHumaConfig`, `WithPublicRoutes` (с nil — ничего). Методы:
  `Router()`, `API()`/`BindAPI()`, `CheckRoutes()`, `Listen()`, `Addr()`, `Serve()`, `Stop(ctx)`.
- **Middleware** (net/http): `RequestID`, `ClientIP(trustProxyHops, log)`,
  `OTel(trustTraceContext, quietPaths...)`, `Recovery(log)`,
  `SecurityHeaders(cfg)`, `CORS(cfg)`, `RateLimit(cfg)`, `MaxBody(cfg)`,
  `AccessLog(log, quietPaths...)`, `Metrics()`,
  `Idempotency(cfg, store, log)`. huma-уровень: `HumaAuth(schemes, log)`.
- **fx**: `Module`, `Middlewares`, `HumaConfigs`, `PublicRoutes` (значения графа),
  `ProvideSecurityScheme(ctor, anns...)` → группа `SecuritySchemeGroup`.
- **Ошибки**: `APIError`, `FieldError`, `Error/BadRequest/Unauthorized/Forbidden/NotFound/
  Conflict/Internal/Unavailable`, `ErrorMap` (`NewErrorMap`, `ErrorRule`, `Map`,
  `Declare`, `Unmapped`), `ProblemContentType`.
- **Auth и маршруты**: `SecuritySchemeEntry` (`Extract` — `AuthExtractor`), `ErrAuthUnavailable`,
  `ErrAuthNotConfigured`, `ContextWithSubject`, `SubjectFrom`, `Public`, `PublicRoutes`, `ErrRoutes`.
- **Health**: `RegisterHealth(api, probeTimeout, log, probes...)`, `Probe`
  (= `healthkit.Probe`); проверка здоровья контейнера — `Healthcheck(ctx)` (код выхода),
  `CheckHealth(ctx, addr)`.
- **Прочее**: `RequestIDFrom`, `ContextWithRequestID`, `HeaderRequestID`,
  `HeaderIdempotentReplayed`, `ClientIPFrom` (в ctx теста без middleware IP кладёт
  `handlertest.ContextWithClientIP`), `IsMaxBodyError`, `NewMemoryStore`, `NewConfig`,
  `Config.Validate`, `ErrConfig`.

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
`Module`; там же — корневой `fx.Invoke`, который сам вешает хук
([architecture.md § Порядок хуков](architecture.md#порядок-хуков)).

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
  `openapi.json` (`Conflict: USER_CONCURRENT_UPDATE`). Ответы, которые кит отдаёт сам,
  операция объявляет сама: 401 с `WWW-Authenticate` при обязательной аутентификации
  (`Unauthorized: UNAUTHORIZED`, коды 401 из `Declare` — рядом), 429 с `Retry-After` при
  лимитере, 503 дедлайна запроса, 413 у тела под лимитом — коды того же статуса из
  таблицы идут первыми и не повторяются
  ([architecture.md § Контракт ошибок](architecture.md#контракт-ошибок)). Свой лимитер
  (`RateLimit` на части путей) объявляет свой 429 правилом таблицы своих операций: при
  выключенном лимитере сервера 429 в контракте останется.
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
IDEMPOTENCY_IN_PROGRESS`. Ответы `5xx` и «не выполнено, повторите» (`408`, `409`, `425`,
`429`) не сохраняются — повтор исполняет запрос заново; запрос или ответ больше
`MaxBodyBytes` идёт без идемпотентности, ошибка store — строка лога
`idempotency store failed` и fail-open (метрика `result=error`) или `503` при
`HTTP_IDEMPOTENCY_FAIL_CLOSED=true`.

Ключ привязан к личности запроса — субъекту, которого кладёт extractor схемы
(`httpkit.ContextWithSubject`, пример — [§ Auth](#auth)). Субъект — стабильный scope: ротация
токена не превращает ретрай в новую операцию и не даёт клиенту новый бюджет ключей.
`Idempotency` стоит в цепочке huma после `HumaAuth`: у защищённой операции replay получает
только запрос, прошедший её схему, extractor зовётся один раз. Идемпотентность — у операций
huma, маршрутам мимо huma её нет.

**Без субъекта дедупликации нет.** Запрос без субъекта идёт сквозным проходом
(`result=skipped`) и в store не пишет: у таких запросов общий scope ключа, а их записи заняли
бы store. Это и аноним — публичная операция (`httpkit.Public`) и запрос, прошедший пустую
альтернативу `{}`, с любыми `Authorization` и `Cookie`, — и клиент схемы, extractor которой
проверил credentials, но субъекта не положил: сами credentials личностью не служат — с каждым
новым токеном клиент получал бы новый scope и новый бюджет ключей. Extractor'ы `oidckit` и
`hubkit/sso` субъекта кладут; свой extractor — строкой
`httpkit.ContextWithSubject(ctx, <id пользователя или клиента>)`, это условие подключения
`IdempotencyStore`. Публичная write-операция выполняется столько раз, сколько раз пришёл
запрос, — закрывайте такие операции `Security` или не опирайтесь на `Idempotency-Key` в их
контракте.

**Бюджет ключей клиента.** Один субъект сохраняет не больше 10 000 ответов за окно
`HTTP_IDEMPOTENCY_TTL` (счёт — в памяти процесса, у каждой реплики свой): один клиент не занимает
store целиком. Предел — на субъекта, а не на всех: `MemoryStore` (100 000 ключей) заполняют
десять учётных записей — сервису с открытой регистрацией нужен store на Redis. Новый ключ сверх
бюджета — как отказ store этому клиенту: строка `idempotency store failed` (`op=reserve`),
`result=error`, запрос идёт без дедупликации или получает `503` при
`HTTP_IDEMPOTENCY_FAIL_CLOSED=true`; сохранённые ответы воспроизводятся.

Подключение store через fx — `Module` навесит middleware с `Config.Idempotency`:

```go
fx.Provide(func(c cachekit.Cache) httpkit.IdempotencyStore { return cachekit.NewIdempotencyStore(c) })
```

Контракт store — байтовый, запись сериализует httpkit. `Reserve` возвращает
токен владельца резерва; `Set` кладёт запись, а `Release` снимает резерв,
**только** пока ключ держит этот токен; `Release` не трогает сохранённый ответ:

```go
type IdempotencyStore interface {
    Reserve(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Set(ctx context.Context, key, token string, record []byte, ttl time.Duration) (stored bool, err error)
    Release(ctx context.Context, key, token string) error
}
```

Токен обязан быть уникальным на каждый успешный `Reserve`: запрос, переживший
`ReserveTTL`, не затирает ни резерв следующего запроса, ни его запись — `Set` с
истёкшим токеном отвечает `stored=false` и ничего не пишет. Свой store проверяется
набором `testkit/idempotencytest.RunContract` — его проходят `httpkit.MemoryStore`
и `cachekit.IdempotencyStore` на памяти и на Redis.

`httpkit.NewMemoryStore()` — in-memory store для тестов и одного процесса:
истёкшие записи выметаются, ключей — не больше 100 000 (новый ключ сверх предела —
ошибка `Reserve`, как у недоступного store: fail-open или 503 по `FailClosed`). При TTL ответа
`24h` предел держит в среднем ~1,2 новых ключа в секунду; больше или свой предел —
`cachekit.NewIdempotencyStore(cachekit.NewMemoryCache(cachekit.WithMaxKeys(n)))` или Redis.

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

Extractor схемы (`SecuritySchemeEntry.Extract`, `func(*http.Request) (context.Context,
error)`) проверяет credentials своей схемы и кладёт личность в ctx; ошибка →
`401 UNAUTHORIZED`, её текст — только в лог. Инфраструктурную ошибку (JWKS/хаб недоступны)
оборачивайте в `httpkit.ErrAuthUnavailable` — клиент получит `503 AUTH_UNAVAILABLE` вместо
разлогина, а операции схемы объявляют его правилом таблицы (`Err: httpkit.ErrAuthUnavailable`):
кит его в OpenAPI не пишет — какой extractor знает недоступный бэкенд, знает только владелец
схемы; свой статус задаётся возвратом `*httpkit.APIError`. Операция со схемой без
extractor'а, в том числе скрытая, не даёт стартовать (`httpkit.ErrAuthNotConfigured` с парами
«операция (схема)»). Своя схема рядом с чужой (hub-сессия, OIDC) — своя запись со своим
extractor'ом; операция, которой подходит любая, перечисляет их альтернативами.

```go
httpkit.ProvideSecurityScheme(func(jwt authnkit.JWTParser, revoked cachekit.Cache) httpkit.SecuritySchemeEntry {
    return httpkit.SecuritySchemeEntry{
        Name:   "Bearer",
        Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
        Extract: func(r *http.Request) (context.Context, error) {
            token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
            claims, err := jwt.ParseAccess(r.Context(), token)
            if err != nil {
                return nil, err // 401, текст — только в лог
            }
            sub, _ := claims["sub"].(string)
            _, gone, err := revoked.Get(r.Context(), "revoked:"+sub)
            if err != nil { // Redis недоступен — 503, а не разлогин
                return nil, fmt.Errorf("%w: %w", httpkit.ErrAuthUnavailable, err)
            }
            if gone {
                return nil, errors.New("subject revoked")
            }
            // Субъект — scope ключа идемпотентности (httpkit.SubjectFrom читает его в хендлере).
            return httpkit.ContextWithSubject(r.Context(), sub), nil
        },
    }
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
{"status":"draining"}`. В OpenAPI оба тела 503 описаны у `/ready` вместе с `APIError`
дедлайна запроса ([architecture.md § Health / readiness](architecture.md#health--readiness)).

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
`ResponseController.SetWriteDeadline`). Дедлайн запроса `HTTP_REQUEST_TIMEOUT` не получает
GET с `Accept: text/event-stream` (`EventSource` шлёт) или `Upgrade` (WebSocket) к маршруту
потока — операции `sse.Register` (ответ `text/event-stream` в её описании) или маршруту мимо
huma; обычной операции huma заголовок клиента дедлайна не снимает. Поток без этого заголовка и
поток на POST ctx отменит на дедлайне — поток делают GET-маршрутом. На
остановке поток держит соединение до дедлайна `HTTP_SHUTDOWN_TIMEOUT`, затем ctx
запроса отменяется — и у WebSocket на захваченном соединении: обработчик, который
слушает `r.Context()`, завершается сам.

## Прямой запуск без fx

```go
s, err := httpkit.NewServer(cfg, log, httpkit.WithSecuritySchemes(bearer)) // цепочка кита, как у Module
if err != nil { /* невалидный Config или схема — ErrConfig */ }
api := s.BindAPI() // HumaAuth — среди первых huma-middleware: Security проверяется extractor'ом
httpkit.RegisterHealth(api, cfg.ReadyProbeTimeout, log)
huma.Register(api, httpkit.Public(huma.Operation{Method: http.MethodGet, Path: "/ping"}), pingHandler)
if err := s.CheckRoutes(); err != nil { /* операции и маршруты — как на старте Module */ }
if err := s.Listen(); err != nil { /* порт занят */ }
go func() { _ = s.Serve() }()
// ...
_ = s.Stop(ctx) // /ready 503, пауза дренажа, дедлайн: ctx хендлеров отменён, соединения закрыты
```
