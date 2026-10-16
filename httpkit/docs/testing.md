# httpkit — тестирование

## Handler'ы через testkit/handlertest

`testkit/handlertest` поднимает huma-API сервера `httpkit` (`NewServer` + `BindAPI` — та же
сборка, что у `Module`, с цепочкой кита целиком: лимитер и лимит тела — по конфигу) под
`humatest` без реального `*http.Server`, с контрактом сервера: ответы-ошибки те же, что в
проде — `request_id`, `Retry-After`, 404/405 маршрутизатора, коды `ErrorMap.Declare` и ответы
кита (401, 429, 503 дедлайна, 413) в OpenAPI; `HumaAuth` стоит до маршрутов; маршруты `register`
проходят проверку старта `Module` (`Server.CheckRoutes`, иначе `t.Fatalf`); конфиг —
`httpkit.NewConfig()`:

```go
import "git.stit.tech/stit-core/golibinfra/testkit/handlertest"

func TestGetUser(t *testing.T) {
    api := handlertest.NewAPI(t, func(api huma.API) {
        registerRoutes(api, svc)
    })

    resp := api.Get("/users/42")
    handlertest.AssertStatus(t, resp, http.StatusOK)

    resp = api.Get("/users/missing")
    handlertest.AssertAPIError(t, resp, http.StatusNotFound, "USER_NOT_FOUND")
}
```

Хелперы: `NewAPI`, `AssertStatus`, `DecodeAPIError` (тело → `httpkit.APIError`:
у валидации 422 поля — в `Errors []httpkit.FieldError{Location, Code, Message}`) и
`AssertAPIError` — статус, `application/problem+json`, `code` и `request_id`,
равный `X-Request-ID` ответа.

То, что сервер берёт из графа, `NewAPI` берёт опциями: `WithSecuritySchemes(entries...)` —
схемы с extractor'ами (`SecuritySchemeEntry.Extract`; схема без него закрывает операцию
`500 AUTH_NOT_CONFIGURED`, схема `Security` вне них, как и операция без `Security` и без
`httpkit.Public` при схемах, валит тест, как старт `Module`), `WithConfig(cfg)` — конфиг вместо
`httpkit.NewConfig()`, `WithLogger(log)` — логгер: причина 5xx — строкой `handler error`
с `request_id` ответа:

```go
api := handlertest.NewAPI(t, func(api huma.API) { registerRoutes(api, svc) },
    handlertest.WithSecuritySchemes(httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: bearer, Extract: extract}))
handlertest.AssertAPIError(t, api.Get("/me"), http.StatusUnauthorized, "UNAUTHORIZED")
```

`HumaAuth` действует на операции, зарегистрированные после него: свой `HumaAuth`
внутри `register` не нужен, и операции, добавленные на API после `NewAPI`, проверку
маршрутов не проходят.

## Полнота маппинга ошибок

Каждый sentinel домена сопоставлен таблицей, а его код — в описании ответа:

```go
func TestUserErrorsComplete(t *testing.T) {
    if left := userErrors.Unmapped(user.ErrNotFound, user.ErrConflict, user.ErrWeakPassword); len(left) > 0 {
        t.Fatalf("unmapped domain errors: %v", left)
    }
}
```

Коды `Declare` видны в `api.OpenAPI()` API из `handlertest.NewAPI` или `Module`:
описание ответа статуса — `Conflict: USER_CONCURRENT_UPDATE`. Лимитер и дедлайн запроса по
умолчанию включены: у каждой операции там же есть 429 и 503, у операции с телом — 413.

## Middleware напрямую

Каждый middleware — `func(http.Handler) http.Handler`, тестируется через
`httptest` без сервера:

```go
h := httpkit.CORS(cfg)(http.HandlerFunc(okHandler))
req := httptest.NewRequest(http.MethodOptions, "/", nil)
req.Header.Set("Origin", "https://example.com")
rec := httptest.NewRecorder()
h.ServeHTTP(rec, req)
```

Для `RateLimit` задайте `req.RemoteAddr` (без `ClientIP` в цепочке ключ — адрес
соединения; с `ClientIP(hops, log)` и `X-Forwarded-For` — запись прокси), чтобы запросы
попали в один bucket; адреса одной IPv6 `/64` — тоже один bucket.

Строки `AccessLog` проверяются логгером в буфер:

```go
var buf bytes.Buffer
log, _ := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
httpkit.AccessLog(log, "/ready")(handler).ServeHTTP(rec, req)
```

## Метрики и спаны

`Metrics()` пишет в глобальный `otel.Meter`: тест читает его через
`testkit/metrictest` — `before := metrictest.Collect(t)`, запросы,
`metrictest.Collect(t).Sub(before).Get(t, "http_requests_total", "method", "GET",
"path", "/users/{id}", "status", "200")`. Label `path` — route-pattern или
`unmatched`, `method` — стандартный метод или `other`.

Имя спана проверяется `tracetest.NewSpanRecorder` c `otel.SetTracerProvider`:
запрос через chi-роутер с `OTel(false)` и `Metrics()` даёт спан `GET /users/{id}` и
атрибуты `http.route`, `request_id`. Судьбу trace-context клиента держит
`TestServer_ClientTraceContextMatrix`: доверие × провайдер трасс (SDK, no-op) × входящий
контекст — `trace_id` записей лога и заголовки вызова `httpclient` из хендлера.

## Идемпотентность

`httpkit.NewMemoryStore()` — in-memory `IdempotencyStore`: первый write-запрос
вызывает handler, повтор с тем же ключом, телом, query и клиентом отдаёт
сохранённый ответ; другое тело или query → `422`. Свой store подставляется
заглушкой с нужной ошибкой — так проверяются fail-open, `FailClosed` и снятие
резерва после ошибки `Set`.

Своя реализация `IdempotencyStore` проверяется общим контрактом — тем же, что
проходят `httpkit.MemoryStore` и `cachekit.IdempotencyStore` на памяти и на Redis:

```go
func TestStore_Contract(t *testing.T) {
    idempotencytest.RunContract(t, func(t *testing.T) httpkit.IdempotencyStore {
        return newStore(t) // изолированный store на каждый кейс
    })
}
```

Кейсы: эксклюзивный резерв, `Get` не отдаёт резерв, запись только по токену
резерва (чужой и истёкший — `stored=false`), `Release` только владельцем и без
удаления записи, истечение записи; кейсы истечения ждут по 400 мс.

## fx-проводка целиком

Поднимите `Module` на свободном порту и ходите по HTTP — так проверяется, что
пробы из группы `healthkit.probes` доезжают до `/ready`, а ошибка handler'а не утекает
клиенту:

```go
t.Setenv("HTTP_ADDR", "127.0.0.1:0")
cfg, _ := httpkit.NewConfig()
var srv *httpkit.Server
app := fx.New(
    fx.Supply(cfg),
    fx.Provide(func() logkit.Logger { return logkit.Nop() }),
    httpkit.Module,
    healthkit.ProvideProbe(func() healthkit.Probe { return failingProbe }),
    fx.Populate(&srv),
)
_ = app.Start(ctx)
resp, _ := http.Get("http://" + srv.Addr().String() + "/ready") // 503
```

## Контракт ошибок в тестах

Форма ошибок — свойство импорта: `init()` пакета подменяет `huma.NewError` и
`huma.NewErrorWithContext`, поэтому в любом тесте, который импортирует `httpkit`
(в том числе через `handlertest`), валидация и 5xx — `APIError`, и 5xx без текста
причины даже на голом `humatest`. `request_id`, `Retry-After` и строку
`handler error` в логе ставит Transformer сервера: их видит API из
`handlertest.NewAPI` или `Module`, а не голый `humatest.New`. Строку лога с
причиной проверяйте логгером в буфер — `handlertest.WithLogger` или логгер графа `Module`
(`TestErrorContract`).
