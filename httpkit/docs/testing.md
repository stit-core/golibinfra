# httpkit — тестирование

## Handler'ы через testkit/handlertest

`testkit/handlertest` поднимает huma-API сервера `httpkit` (`NewServer` +
`BindAPI`, из цепочки — только `RequestID`) под `humatest` без реального
`*http.Server`: ответы-ошибки те же, что в проде — `request_id`, `Retry-After`,
404/405 маршрутизатора, коды `ErrorMap.Declare` в OpenAPI:

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

`HumaAuth` работает на любом huma-адаптере, поэтому auth-гейт тестируется прямо
на `handlertest.NewAPI`:

```go
api := handlertest.NewAPI(t, func(api huma.API) {
    api.UseMiddleware(httpkit.HumaAuth(logkit.Nop(), extract))
    registerRoutes(api, svc)
})
handlertest.AssertAPIError(t, api.Get("/me"), http.StatusUnauthorized, "UNAUTHORIZED")
```

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
описание ответа статуса — `Conflict: USER_CONCURRENT_UPDATE`.

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

Для `RateLimit` задайте `req.RemoteAddr` (или `X-Forwarded-For` с `TrustProxy`,
где ключом станет **правое** значение), чтобы запросы попали в один bucket.

Строки `AccessLog` проверяются логгером в буфер:

```go
var buf bytes.Buffer
log, _ := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
httpkit.AccessLog(log, "/ready")(handler).ServeHTTP(rec, req)
```

## Метрики и спаны

`Metrics()` пишет в глобальный `otel.Meter`: в тесте зарегистрируйте
`sdkmetric.NewMeterProvider` с `ManualReader` через `otel.SetMeterProvider` до
создания middleware и соберите `ResourceMetrics`. Label `path` — route-pattern
или `unmatched`, `method` — стандартный метод или `other`.

Имя спана проверяется `tracetest.NewSpanRecorder` c `otel.SetTracerProvider`:
запрос через chi-роутер с `OTel()` и `Metrics()` даёт спан `GET /users/{id}` и
атрибуты `http.route`, `request_id`.

## Идемпотентность

`httpkit.NewMemoryStore()` — in-memory `IdempotencyStore`: первый write-запрос
вызывает handler, повтор с тем же ключом, телом, query и клиентом отдаёт
сохранённый ответ; другое тело или query → `422`. Свой store подставляется
заглушкой с нужной ошибкой — так проверяются fail-open, `FailClosed` и снятие
резерва после ошибки `Set`.

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
    healthkit.Provide(func() healthkit.Probe { return failingProbe }),
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
причиной проверяйте на `Module` с логгером в буфер (`TestErrorContract`).
