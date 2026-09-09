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
`handlertest.APIError` — локальная структура для декода, не `httpkit.APIError`:
она покрывает оба тела сразу — плоский problem+json `httpkit.Error(...)` (`code`,
`status`, `title`, `detail`) и `huma.ErrorModel` валидации 422, где список
проблем приходит в `Errors []APIErrorLocation{Message, Location, Value}`.

`HumaAuth` работает на любом huma-адаптере, поэтому auth-гейт тестируется прямо
на `handlertest.NewAPI`:

```go
api := handlertest.NewAPI(t, func(api huma.API) {
    api.UseMiddleware(httpkit.HumaAuth(logkit.Nop(), extract))
    registerRoutes(api, svc)
})
handlertest.AssertAPIError(t, api.Get("/me"), http.StatusUnauthorized, "UNAUTHORIZED")
```

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

## Инвариант error-hook'а

Нейтрализация 5xx (`handler error` в лог, `{"code":"INTERNAL"}` клиенту) живёт в
process-global переменной huma (`huma.NewErrorWithContext`) и ставится **любым**
`NewServer`, с `WithLogger` и без: опция определяет только, куда уходит текст
ошибки. В тесте на голом `handlertest.NewAPI`, где `Server` в процессе не
создавался, действует дефолт huma. Проверяйте отсутствие утечки на fx-проводке
целиком; сценарий «без fx» проверяется в отдельном процессе, иначе хук ставит
любой другой тест того же пакета.
