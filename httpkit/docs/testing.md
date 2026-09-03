# httpkit — тестирование

## Handler'ы через testkit/handlertest

`testkit/handlertest` поднимает huma-API на `humatest` без реального
`*http.Server`:

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

Хелперы: `NewAPI`, `AssertStatus`, `DecodeAPIError`, `AssertAPIError`.
`handlertest.APIError` — локальная структура для декода, не `httpkit.APIError`.

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

Для `RateLimit` задайте `req.RemoteAddr` (или `X-Forwarded-For` с
`TrustProxy`), чтобы запросы попали в один bucket.

## Метрики

`Metrics()` пишет в глобальный `otel.Meter`: в тесте зарегистрируйте
`sdkmetric.NewMeterProvider` с `ManualReader` через `otel.SetMeterProvider` до
создания middleware и соберите `ResourceMetrics`. Label `path` — route-pattern
или `unmatched`.

## Идемпотентность

`httpkit.NewMemoryStore()` — in-memory `IdempotencyStore`: первый write-запрос
вызывает handler, повтор с тем же ключом, телом и `Authorization` отдаёт
сохранённый ответ; другое тело → `422`.

## fx-проводка целиком

Поднимите `Module` на свободном порту и ходите по HTTP — так проверяется, что
пробы из группы `health` доезжают до `/ready`:

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
