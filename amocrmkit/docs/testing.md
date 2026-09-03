# amocrmkit — тестирование

У кита нет отдельного testkit-сабпакета и нет in-memory фейка `Client`: он
обращается к amoCRM по HTTP, поэтому тестируется через `httptest.Server`. Два
шва для подмены бэкенда: редирект хоста (`Config.HostOverride`) и базовый
транспорт (`WithTransport`).

## Почему HostOverride, а не интерфейс

`Client` — конкретный тип, а не интерфейс. Для проверки реального пути (валидация
координат, сборка URL, заголовки, подпись amoJo, классификация ошибок, декод
`_embedded`) тест поднимает `httptest.Server` и направляет на него весь трафик
клиента через `HostOverride` (scheme://host). Путь, query, заголовки и подпись
при этом сохраняются — проверяется именно то, что уйдёт в прод.

```go
hcCfg := httpclient.Config{
    Timeout:    5 * time.Second,
    MaxRetries: 0, // в тестах ретраи обычно отключают
    RetryWait:  time.Millisecond,
    Breaker: httpclient.BreakerConfig{
        FailureThreshold: 100,
        RecoveryTimeout:  time.Second,
    },
}
c, _ := amocrmkit.New(
    amocrmkit.Config{
        RateRPS:      1000,    // высокий — чтобы тесты не троттлились
        HostOverride: srv.URL, // весь трафик → httptest
    },
    hcCfg, clockkit.Real{}, logkit.Nop(),
)
```

`HostOverride` редиректит **все** контуры (REST/amoJo/OAuth/Drive) на один хост —
один `httptest.Server` обслуживает любой из них; маршрутизируйте по
`r.Method + " " + r.URL.Path`. Координаты в тестах должны быть валидными
(`Subdomain` по `^[a-z0-9-]{1,63}$`, `TLD` `ru`/`com`) — иначе вызов упадёт с
`ErrInvalidAccount` до сети.

`WithTransport(rt)` подставляет транспорт под собираемый китом httpclient —
удобно, когда нужен `http.RoundTripper`-стаб без сервера. `WithHTTPClient(hc)`
подставляет готовый `*httpclient.Client` целиком (его ретраи лимитер аккаунта не
видят).

## Что проверять на стороне сервера

- **REST**: заголовок `Authorization: Bearer <token>`, query-параметры
  (`limit`, `with`, `filter[...]`), тело батча; отдавать конверт с `_embedded`,
  `_page`, `_links`.
- **amoJo**: заголовки `Date`, `Content-Type: application/json`, `Content-MD5`,
  `X-Signature`. Подпись пересчитывается по 5-компонентной строке
  `UPPER(method)\nContent-MD5\napplication/json\nDate\npath` и сверяется с
  `X-Signature` (см. `assertAmojoSignature` в `amojo_test.go`).
- **OAuth**: тело POST `/oauth2/access_token` (`grant_type`, `code`/`refresh_token`,
  обязательный `redirect_uri`); ответ с `server_time`/`expires_in`.

## Классификация ошибок

Сервер возвращает нужный статус + тело `application/problem+json`; тест проверяет
sentinel через `errors.Is` и деталь через `errors.As`:

```go
srv := /* отдаёт 404 с {"title":"Not Found","detail":"nope"} */
_, err := c.ListTasks(ctx, acc, amocrmkit.ListParams{})

if !errors.Is(err, amocrmkit.ErrNotFound) { /* fail */ }

var apiErr *amocrmkit.APIError
if errors.As(err, &apiErr) {
    // apiErr.Detail == "nope"
}
```

`429` несёт `retry_after` как поле тела (не только заголовок) — `APIError.RetryAfter`
парсит оба источника.

## Детерминированное время

`clockkit` инъектируется в `New` — для проверки TTL-логики (`Tokens.ExpiresAt`),
фиксированного `Date` в amoJo-подписи или вытеснения лимитеров подставляйте
`clockkit.Fixed`/`clockkit.Tickable` вместо `clockkit.Real{}`. Подпись
(`signRequest`) принимает `now time.Time` явно, что делает её юнит-тестируемой
без клиента.

## Rate-limiter

`accountLimiter` тестируется напрямую (см. `ratelimit_test.go`): per-account
изоляция бюджета, один `*rate.Limiter` на аккаунт, реакция `wait` на отменённый
`ctx`, вытеснение аккаунтов, молчавших дольше TTL (через `clockkit.Tickable`), и
хук `beforeAttempt` (первая попытка не ждёт, ретрай — ждёт). Проход ретрая
httpclient через лимитер проверяется интеграционно по времени двух попыток. В
интеграционных тестах поднимайте `RateRPS` высоко, чтобы троттлинг не
вмешивался; для проверки самого троттлинга задавайте низкий `RateRPS` и считайте
`Allow()`.

## Верификация вебхуков

`VerifyWebhookSignature` — чистая функция, тестируется без сервера: подпись по
сырому телу, регистронезависимое сравнение hex, отказ при неверном секрете,
изменённом теле или префиксе `sha1=` (amoJo шлёт голый hex).
