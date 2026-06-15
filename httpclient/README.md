# httpclient

Outbound HTTP-клиент для сервисов stit: retry с экспоненциальным backoff,
circuit breaker (`sony/gobreaker/v2`), OTel-инструментирование через
`otelhttp.Transport` и автоматическое прокидывание `X-Request-ID` из контекста
запроса.

Единый источник правды по этому киту — исходники (`httpclient/*.go`) и этот файл.
Версионирование, доступ к репозиторию и общие принципы — в
[корневом `README.md`](../README.md).

## Назначение

`*httpclient.Client` — обёртка над `*http.Client`, которая закрывает четыре
сквозные заботы исходящих вызовов одним местом:

- **Retry.** Повтор на 5xx и сетевых ошибках с экспоненциальным backoff + jitter.
- **Circuit breaker.** После серии подряд идущих провалов клиент перестаёт ходить
  на упавший upstream и быстро отдаёт ошибку, давая сервису восстановиться.
- **Observability.** Спан клиента (`otelhttp`) + метрики длительности, retry и
  переходов breaker'а из коробки.
- **Trace correlation.** `X-Request-ID` из контекста проставляется в исходящий
  запрос, чтобы цепочка вызовов сшивалась через корп-стек.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/httpclient"
```

## Быстрый старт

### Через fx

`Module` провайдит `*httpclient.Client` из `httpclient.Config`. Сам `Config` модуль
**не** провайдит (asymmetry-нота — как в `pgkit`): приложение либо собирает
`Config` само, либо подключает `httpclient.NewConfig` (читает ENV через envconfig).

```go
fx.New(
    telemetry.Module, // meter/tracer для метрик и спанов клиента
    fx.Provide(httpclient.NewConfig),
    httpclient.Module, // provide: *httpclient.Client
    fx.Invoke(func(c *httpclient.Client) {
        // c готов к использованию
    }),
)
```

### Прямой конструктор

```go
client := httpclient.New(httpclient.Config{
    Timeout:    10 * time.Second,
    MaxRetries: 3,
    RetryWait:  200 * time.Millisecond,
    Breaker: httpclient.BreakerConfig{
        FailureThreshold: 5,
        RecoveryTimeout:  30 * time.Second,
        SuccessThreshold: 3,
    },
})
```

### Выполнение запроса

Интерфейс совпадает с `*http.Client` в части `Do`:

```go
func (c *Client) Do(req *http.Request) (*http.Response, error)
```

```go
req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
if err != nil {
    return err
}
resp, err := client.Do(req)
if err != nil {
    return err // сетевая ошибка, исчерпанные retry или открытый breaker
}
defer resp.Body.Close()
// resp.StatusCode может быть 4xx — на клиентские ошибки retry не делается
```

Точные сигнатуры:

```go
func New(cfg Config) *Client
func NewConfig() (Config, error)
func (c *Client) Do(req *http.Request) (*http.Response, error)
```

## Конфигурация

Структуры `httpclient.Config` и `httpclient.BreakerConfig` — **part of public
API**: имена/типы полей и их envconfig-теги менять = breaking change (идёт мажорным
апгрейдом). Список public-конфигов — корневой [`README.md` § Стабильность API](../README.md#стабильность-api).

`NewConfig` читает всё через `envconfig.Process("", &c)`. `BreakerConfig` вложен в
`Config` без префикса, поэтому теги breaker'а заданы полными именами.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `HTTPCLIENT_TIMEOUT` | `Config.Timeout` | `time.Duration` | `10s` | Таймаут одной HTTP-попытки (`http.Client.Timeout`) — на каждую попытку, а не на весь цикл retry |
| `HTTPCLIENT_MAX_RETRIES` | `Config.MaxRetries` | `int` | `3` | Кол-во повторов сверх первой попытки (`0` → одна попытка без retry) |
| `HTTPCLIENT_RETRY_WAIT` | `Config.RetryWait` | `time.Duration` | `200ms` | База backoff: задержка перед повтором растёт как `RetryWait·2^attempt` + jitter |
| `HTTPCLIENT_BREAKER_FAILURE_THRESHOLD` | `BreakerConfig.FailureThreshold` | `int` | `5` | Сколько подряд идущих провалов переводят breaker в Open |
| `HTTPCLIENT_BREAKER_RECOVERY_TIMEOUT` | `BreakerConfig.RecoveryTimeout` | `time.Duration` | `30s` | Сколько breaker держится в Open до перехода в Half-Open (пробный запрос) |
| `HTTPCLIENT_BREAKER_SUCCESS_THRESHOLD` | `BreakerConfig.SuccessThreshold` | `int` | `3` | Поле public-конфига; в текущей проводке `gobreaker.Settings` напрямую не используется (см. § Инварианты и подводные камни) |

## Observability

Метрики регистрируются на глобальном meter'е
`git.stit.tech/stit-core/golibinfra/httpclient`. Спан клиента ведёт
`otelhttp.Transport`, обёрнутый вокруг `http.DefaultTransport` — то есть клиентский
HTTP-спан и context-propagation работают автоматически, без отдельной настройки в
коде потребителя.

| Сигнал | Тип | Labels | Назначение |
|---|---|---|---|
| `http_client_request_duration_seconds` | Float64 Histogram (`s`) | `host`, `method`, `status` | Длительность исходящего запроса целиком (включая retry-цикл и время, проведённое внутри breaker'а). `status` — код ответа или `error`, если ответа нет |
| `http_client_retry_attempts_total` | Int64 Counter | `host` | Инкремент на каждый повтор (первая попытка не считается) |
| `http_client_breaker_transitions_total` | Int64 Counter | `name`, `from`, `to` | Переход состояния breaker'а; `name` фиксирован — `httpclient` |
| `http_client_breaker_open` | Int64 UpDownCounter | `name` | `1`, пока breaker открыт; `0` в Closed/Half-Open. Ведётся дельтами по `OnStateChange` |
| client span | OTel span (`otelhttp`) | — | Клиентский HTTP-спан; несёт W3C trace-context в upstream |

Reference по всем китам — корневой [`README.md` § Что эмитит каждый пакет](../README.md#что-эмитит-каждый-пакет-короткий-reference).
Resource attrs (`service.*`, `tenant`, `service.namespace`) к этим метрикам
добавляет otel-collector — см. там же.

## API

### `Client`

`*Client` создаётся через `New(cfg)` (или провайдится `Module`) и предоставляет
единственный метод `Do(req)` с сигнатурой `*http.Client`. Внутри один вызов `Do`:

1. проставляет `X-Request-ID` из `req.Context()` (через `httpkit.RequestIDFrom`),
   если он ещё не пуст и заголовок не задан явно;
2. оборачивает весь retry-цикл в `breaker.Execute` — breaker считает успехом
   только результат без ошибки;
3. записывает `http_client_request_duration_seconds` по итогу (даже при ошибке).

`Client` потокобезопасен и переиспользуется на всё время жизни приложения — один
инстанс на upstream-направление (breaker и его счётчики — общие для инстанса).

### `Config` / `BreakerConfig`

`Config` агрегирует параметры таймаута и retry плюс вложенный `BreakerConfig`.
`BreakerConfig` отделён, потому что breaker — самостоятельная политика «когда
перестать ходить на упавший upstream»; держать её отдельной структурой удобно для
явной конфигурации в коде и для группировки envconfig-тегов
`HTTPCLIENT_BREAKER_*`.

Когда настраивать что:

- **Лезущие наружу вызовы с нестабильным upstream** — подними
  `FailureThreshold`/`RecoveryTimeout`, чтобы breaker не «дёргался» на разовых
  сбоях.
- **Жёсткий бюджет латентности** — снижай `Timeout` и `MaxRetries`: суммарное
  время вызова ограничено сверху примерно `Timeout · (MaxRetries+1)` плюс backoff
  между попытками.

## Тестирование

Юнит-тесты кита (`client_test.go`, `breaker_test.go`) гоняют клиент против
`httptest.Server` — это же рекомендуемый способ покрывать использование клиента в
сервисе:

- **Retry на 5xx.** Хендлер отдаёт 500 первые N вызовов, затем 200; проверяется,
  что счётчик вызовов сервера дошёл до успешного ответа (retry сработал).
- **Исчерпание retry.** Хендлер всегда отдаёт 5xx; `Do` возвращает ошибку после
  `MaxRetries` повторов.
- **Отсутствие retry на 2xx.** Хендлер отдаёт 200; сервер получает ровно один
  вызов.
- **Circuit breaker.** После `FailureThreshold` провалов следующий `Do`
  возвращает `gobreaker.ErrOpenState` без обращения к серверу; после
  `RecoveryTimeout` пробный запрос проходит.
- **`X-Request-ID`.** Явно установленный заголовок доезжает до сервера в
  неизменном виде.
- **Таймаут.** При медленном хендлере и малом `Timeout` `Do` возвращает ошибку.

`gobreaker.ErrOpenState` (`github.com/sony/gobreaker/v2`) — то, что возвращает
`Do`, когда breaker открыт; на него удобно матчить через `errors.Is`.

## Инварианты и подводные камни

- **Retry только на идемпотентно-безопасных исходах.** Повтор делается на сетевых
  ошибках и ответах `>= 500`. На `4xx` (и любой `< 500`) retry **нет** — это не
  ошибка транспорта. Контекстные ошибки (`context.Canceled`/`DeadlineExceeded`)
  не ретраятся, а возвращаются сразу. Клиент не различает идемпотентность по
  HTTP-методу — если повтор POST/PATCH небезопасен для конкретного upstream,
  ставь `MaxRetries: 0` для такого клиента либо защищайся идемпотентным ключом на
  стороне сервера.
- **Тело ответа на 5xx закрывается перед повтором.** `doWithRetry` вызывает
  `resp.Body.Close()` перед следующей попыткой, поэтому потребитель не утекает
  соединения между retry; закрыть `resp.Body` финального успешного ответа —
  обязанность вызывающего.
- **Backoff = `RetryWait · 2^attempt` + jitter.** Растёт экспоненциально; jitter
  (`rand` в диапазоне `[0, RetryWait)`) размазывает ретраи, чтобы не было
  thundering herd. Jitter некриптографический и для backoff достаточен.
- **Состояния circuit breaker.** Closed → запросы идут. После
  `FailureThreshold` **подряд** идущих провалов → Open: `Do` мгновенно отдаёт
  `gobreaker.ErrOpenState`, не трогая upstream. По истечении `RecoveryTimeout` →
  Half-Open: пропускается пробный запрос; успех закрывает breaker, провал снова
  открывает. Breaker считает успехом результат без `error` — то есть исчерпанный
  retry-цикл (вернувший ошибку) breaker видит как провал.
- **`SuccessThreshold` сейчас не пробрасывается в `gobreaker.Settings`.** Поле
  присутствует в `BreakerConfig` (part of public API) и читается из ENV, но в
  текущей проводке `newBreaker` не выставляет `gobreaker.Settings.MaxRequests` из
  него — переход Half-Open → Closed управляется дефолтом gobreaker. Закладываться
  на тонкую настройку числа пробных запросов через это поле не стоит.
- **`X-Request-ID` propagation не перетирает явный заголовок.** ID берётся из
  контекста (`httpkit.HeaderRequestID`) и проставляется в исходящий запрос; если
  заголовок уже выставлен на запросе вручную, остаётся он. Чтобы ID появился
  автоматически, контекст запроса должен нести request-id (его кладёт middleware
  `httpkit`).
- **`Timeout` — на попытку, не на весь вызов.** Это `http.Client.Timeout`,
  применяемый к каждой попытке отдельно. Суммарный бюджет на `Do` (с retry и
  backoff) больше: ограничивай его таймаутом/дедлайном на `req.Context()` — он
  обрывает retry-цикл между попытками.
- **Один breaker на инстанс `Client`.** Счётчики провалов и состояние breaker'а
  общие для всех запросов через данный `Client` (имя breaker'а фиксировано —
  `httpclient`). Для изоляции разных upstream'ов заводи отдельные инстансы
  `Client`.
