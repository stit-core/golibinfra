# httpkit — тестирование

## Тестирование handler'ов через testkit/handlertest

`testkit/handlertest` поднимает huma-API на `humatest` и предоставляет хелперы
для проверки ответов и `APIError`. Это путь для consumer-сервисов: тестировать
свои роуты, не поднимая реальный `*http.Server`.

```go
import "git.stit.tech/stit-core/golibinfra/testkit/handlertest"

func TestGetUser(t *testing.T) {
    api := handlertest.NewAPI(t, func(api huma.API) {
        registerRoutes(api, svc) // ваши huma.Get/huma.Register
    })

    resp := api.Get("/users/42")
    handlertest.AssertStatus(t, resp, http.StatusOK)

    // Для ошибочного ответа — проверка статуса + APIError.Code разом:
    resp = api.Get("/users/missing")
    handlertest.AssertAPIError(t, resp, http.StatusNotFound, "USER_NOT_FOUND")
}
```

Хелперы:

- `NewAPI(t, register) API` — `humatest.TestAPI` с зарегистрированными роутами.
- `AssertStatus(t, resp, want)` — проверка HTTP-кода (при несовпадении печатает
  тело).
- `DecodeAPIError(t, resp) APIError` — разбирает тело как `{code,status,title,
  detail}`; понимает и huma-обёртку `{"errors":[...]}`, и прямой объект.
- `AssertAPIError(t, resp, status, code)` — статус + `code` за один вызов.

`handlertest.APIError` — локальная структура для декода (поля `code`, `status`,
`title`, `detail`), не путать с `httpkit.APIError`.

## Тестирование middleware напрямую

Каждый middleware — обычный `func(http.Handler) http.Handler`, тестируется через
`net/http/httptest` без сервера:

```go
h := httpkit.CORS(cfg)(http.HandlerFunc(okHandler))
req := httptest.NewRequest(http.MethodOptions, "/", nil)
req.Header.Set("Origin", "https://example.com")
rec := httptest.NewRecorder()
h.ServeHTTP(rec, req)
// rec.Code, rec.Header() ...
```

RateLimit удобнее проверять через `httptest.NewServer` (нужен реальный
`RemoteAddr` для per-IP bucket).

## Метрики в тестах

`Metrics(meter)` принимает `metric.Meter` аргументом — подайте
`sdkmetric.NewManualReader` + `NewMeterProvider`, прогоните запрос и соберите
`ResourceMetrics`, чтобы проверить `http_requests_total` /
`http_request_duration_seconds` и их labels (`method`/`path`/`status`).

## Идемпотентность в тестах

`httpkit.NewMemoryStore(ttl)` — in-memory `IdempotencyStore` для тестов: первый
write-запрос вызывает handler, повторный с тем же `Idempotency-Key` отдаёт
закэшированный ответ без повторного вызова. GET игнорируется.
