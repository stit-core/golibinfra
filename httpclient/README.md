# httpclient

Исходящий HTTP-клиент для сервисов stit: retry с экспоненциальным backoff,
circuit breaker на каждый upstream-хост (`sony/gobreaker/v2`), OTel-спаны через
`otelhttp.Transport`, прокидывание `X-Request-ID` из контекста и SSE-стримы для
long-lived соединений. Один `*httpclient.Client` обслуживает любое число хостов.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/httpclient"
```

## Быстрый старт

```go
fx.New(
    fx.Provide(httpclient.NewConfig), // HTTPCLIENT_* из ENV (caller провайдит явно)
    httpclient.Module,                // *httpclient.Client

    fx.Invoke(func(hc *httpclient.Client) {
        req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/v1/items", nil)
        resp, err := hc.Do(req)      // retry + breaker; 5xx после ретраев — это ответ, не ошибка
        if err != nil { return }     // транспорт, ctx или httpclient.ErrBreakerOpen
        defer resp.Body.Close()
    }),
)
```

Без fx:

```go
hc, err := httpclient.New(cfg,
    httpclient.WithTransport(customTransport),                 // TLS/proxy/тесты
    httpclient.WithBeforeAttempt(func(ctx context.Context, attempt int) error {
        return limiter.Wait(ctx)                               // rate-limit на каждую попытку
    }),
)
```

## Конфигурация

`httpclient.Config` и `httpclient.BreakerConfig` — public API (см. корневой
`README.md` § Стабильность API). `NewConfig` читает ENV и валидирует
(`ErrConfig`); `New` валидирует ещё раз для конфигов, собранных вручную.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTPCLIENT_TIMEOUT` | `Config.Timeout` | `time.Duration` | `10s` | Таймаут одной попытки (`http.Client.Timeout`), не всего цикла retry |
| `HTTPCLIENT_MAX_RETRIES` | `Config.MaxRetries` | `int` | `3` | Повторов сверх первой попытки; `0` — без retry |
| `HTTPCLIENT_RETRY_WAIT` | `Config.RetryWait` | `time.Duration` | `200ms` | База backoff: пауза перед повтором `RetryWait·2^(attempt-1)` + jitter `[0, RetryWait)`; обязателен `> 0` при `MaxRetries > 0` |
| `HTTPCLIENT_RETRY_MAX_WAIT` | `Config.RetryMaxWait` | `time.Duration` | `5s` | Потолок паузы, в том числе для `Retry-After`; `0` — без потолка |
| `HTTPCLIENT_RETRY_NON_IDEMPOTENT` | `Config.RetryNonIdempotent` | `bool` | `false` | Ретраить POST/PATCH без явного разрешения (см. § Retry) |
| `HTTPCLIENT_BREAKER_FAILURE_THRESHOLD` | `BreakerConfig.FailureThreshold` | `int` | `5` | Подряд идущих провалов до Open; `0` выключает breaker |
| `HTTPCLIENT_BREAKER_RECOVERY_TIMEOUT` | `BreakerConfig.RecoveryTimeout` | `time.Duration` | `30s` | Сколько breaker держится в Open до пробного запроса |

## Retry

Повтор делается только когда он безопасен:

- метод идемпотентен (`GET`, `HEAD`, `OPTIONS`, `PUT`, `DELETE`, `TRACE`), либо
  запрос явно разрешил повтор: `httpclient.AllowRetry(ctx)`, заголовок
  `Idempotency-Key`, либо `HTTPCLIENT_RETRY_NON_IDEMPOTENT=true`;
- тело можно перечитать: `req.GetBody != nil` (`http.NewRequest` ставит его для
  `bytes.Reader`, `strings.Reader`, `bytes.Buffer`). Потоковое тело без `GetBody`
  не ретраится.

Ретраятся транспортные ошибки (кроме отмены/дедлайна ctx) и статусы `408`,
`429`, `500`, `502`, `503`, `504`. Пауза — `Retry-After` сервера (секунды или
HTTP-дата), иначе экспоненциальный backoff с jitter; и то и другое под
`RetryMaxWait`. После исчерпания ретраев возвращается **последний ответ** с его
статусом и телом (`err == nil`): статус разбирает вызывающий.

`WithBeforeAttempt` вызывается перед каждой попыткой, включая первую, с номером
попытки от нуля: место для per-host rate-limiter'а. Ошибка hook'а прерывает запрос.

## Circuit breaker

Breaker заводится на каждый `req.URL.Host`: отказ одного upstream не размыкает
цепь остальным. Провалом считается транспортная ошибка или ответ `5xx` после
ретраев. В открытом состоянии `Do` возвращает `httpclient.ErrBreakerOpen`
(оборачивает `gobreaker.ErrOpenState` / `ErrTooManyRequests`).

## SSE

`Stream(req)` открывает `text/event-stream` без `Timeout`, retry и breaker'а:
стрим живёт, пока жив `req.Context()`, а `SSEStream.Close()` останавливает
фоновую горутину. События — по спеке WHATWG (`ID`, `Event`, `Data`); терминатор
вроде `[DONE]` приходит обычным событием, его интерпретирует вызывающий.

## Observability

Instrumentation-scope `git.stit.tech/stit-core/golibinfra/httpclient`, глобальные
OTel-провайдеры (no-op без `MeterProvider`/`TracerProvider`).

| Сигнал | Тип | Labels | Назначение |
|---|---|---|---|
| `http_client_request_duration_seconds` | Float64 Histogram (`s`) | `host`, `method`, `status` | Длительность `Do` целиком, включая ретраи и ожидание breaker'а; `status` — код ответа или `error` |
| `http_client_retry_attempts_total` | Int64 Counter | `host` | Инкремент на каждый повтор |
| `http_client_breaker_transitions_total` | Int64 Counter | `host`, `from`, `to` | Переходы состояния breaker'а хоста |
| `http_client_breaker_open` | Int64 UpDownCounter | `host` | `1`, пока breaker хоста открыт |
| client span | OTel span (`otelhttp`) | — | Клиентский HTTP-спан на каждую попытку, W3C trace-context в upstream |

`host` ограничен числом upstream'ов сервиса; `method`/`status` — конечные множества.

## API

```go
func New(cfg Config, opts ...Option) (*Client, error)
func NewConfig() (Config, error)
func (c Config) Validate() error

func WithTransport(rt http.RoundTripper) Option
func WithBeforeAttempt(fn BeforeAttemptFunc) Option
type BeforeAttemptFunc func(ctx context.Context, attempt int) error

func AllowRetry(ctx context.Context) context.Context

func (c *Client) Do(req *http.Request) (*http.Response, error)
func (c *Client) Stream(req *http.Request) (*SSEStream, error)

var ErrConfig, ErrBreakerOpen error
```

## Тестирование

`go test -short -race ./httpclient/...` — httptest-серверы, без сети. Для тестов
потребителя удобен `WithTransport` с `httptest`-транспортом или подмена URL.

## Инварианты и подводные камни

- **5xx — не ошибка `Do`.** Ошибка только у транспорта, ctx и открытого
  breaker'а; статус разбирает вызывающий. Тело последнего ответа читаемо.
- **POST не ретраится по умолчанию.** Дубли на стороне сервера дороже повтора;
  разрешайте явно через `AllowRetry`/`Idempotency-Key`, когда сервер
  дедуплицирует.
- **`Timeout` — на попытку.** Общее время `Do` может достигать
  `(MaxRetries+1)·Timeout + Σ backoff`; ограничивайте дедлайном ctx.
- **Breaker на хост считает 5xx провалом.** Upstream, стабильно отвечающий
  `503`, откроет цепь через `FailureThreshold` запросов; полностью выключить —
  `HTTPCLIENT_BREAKER_FAILURE_THRESHOLD=0`.
- **`X-Request-ID`** ставится из ctx только если вызывающий не задал свой заголовок.
