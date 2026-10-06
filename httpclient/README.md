# httpclient

Исходящий HTTP-клиент для сервисов stit: retry с экспоненциальным backoff,
circuit breaker на каждый upstream-хост (`sony/gobreaker/v2`), политика
редиректов, SSE-стримы и OTel-инструментация (спан на вызов + спаны попыток с
W3C trace-context, метрики, `X-Request-ID` из контекста; секреты URL — `xxxxx`). Один
`*httpclient.Client` обслуживает много хостов: свой breaker и label `host` — у
первых `HTTPCLIENT_MAX_HOSTS`, остальные идут без breaker'а с `host="other"`.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/httpclient"
```

## Быстрый старт

```go
fx.New(
    fx.Provide(httpclient.NewConfig), // HTTPCLIENT_* из ENV (caller провайдит явно)
    httpclient.Module,                // *httpclient.Client, idle-соединения закрываются на OnStop

    // Опции, которые нельзя задать через ENV, — одним значением, в порядке слайса:
    fx.Provide(func(rl *Limiter, q *Quota) httpclient.Options { // Limiter, Quota — типы сервиса
        return httpclient.Options{
            httpclient.WithBeforeAttempt(func(ctx context.Context, _ int) error { return rl.Wait(ctx) }),
            httpclient.WithBeforeAttempt(q.Take), // hook'и складываются: сначала лимитер, затем квота
        }
    }),

    fx.Invoke(func(hc *httpclient.Client) error {
        ctx := context.Background() // в обработчике — ctx запроса
        req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/v1/items", nil)
        if err != nil {
            return err
        }
        resp, err := hc.Do(req)      // retry + breaker; 5xx после ретраев — это ответ, не ошибка
        if err != nil {              // транспорт, ctx или httpclient.ErrBreakerOpen
            return err
        }
        defer resp.Body.Close()
        return nil
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
defer hc.Close()
```

Несколько клиентов с разной политикой в одном процессе — `NewConfigPrefixed`:
`NewConfigPrefixed("AMOCRM")` читает `<PREFIX>_HTTPCLIENT_*`, в том числе breaker —
`<PREFIX>_HTTPCLIENT_BREAKER_*`, и падает обратно на голые `HTTPCLIENT_*` для
незаданных переменных (базовая политика сервиса + точечные переопределения на
upstream).

## Конфигурация

`httpclient.Config` и `httpclient.BreakerConfig` — public API (см. корневой
`README.md` § Стабильность API). `NewConfig`/`NewConfigPrefixed` читают ENV и
валидируют (`ErrConfig`); `New` валидирует ещё раз для конфигов, собранных
вручную. Дефолты — в тегах: ноль в литерале — значение, а не дефолт, поэтому клиент без
таймаута, паузы ретрая или предела события SSE (в том числе `httpclient.Config{}`) собрать
нельзя.

| Env | Поле | Тип | Default | Правило `Validate` |
|---|---|---|---|---|
| `HTTPCLIENT_TIMEOUT` | `Config.Timeout` | `time.Duration` | `10s` | `> 0`, иначе ошибка. Таймаут одной попытки (`http.Client.Timeout`), не всего цикла retry; для `Stream` — только фаза заголовков |
| `HTTPCLIENT_MAX_RETRIES` | `Config.MaxRetries` | `int` | `3` | `< 0` — ошибка; `0` — без retry (осознанный выбор, дефолт не подставляется) |
| `HTTPCLIENT_RETRY_WAIT` | `Config.RetryWait` | `time.Duration` | `200ms` | `> 0`, иначе ошибка. База backoff: `RetryWait·2^(attempt-1)` + jitter `[0, RetryWait)` |
| `HTTPCLIENT_RETRY_MAX_WAIT` | `Config.RetryMaxWait` | `time.Duration` | `5s` | `< RetryWait` — ошибка. Потолок паузы, в том числе для `Retry-After` |
| `HTTPCLIENT_RETRY_NON_IDEMPOTENT` | `Config.RetryNonIdempotent` | `bool` | `false` | Ретраить POST/PATCH без явного разрешения (см. § Retry) |
| `HTTPCLIENT_MAX_REDIRECTS` | `Config.MaxRedirects` | `int` | `10` | `< 0` — ошибка; `0` — любой редирект `ErrTooManyRedirects`. Длиннее цепочка — `ErrTooManyRedirects` |
| `HTTPCLIENT_ALLOW_CROSS_HOST_REDIRECTS` | `Config.AllowCrossHostRedirects` | `bool` | `false` | `false` — редирект на другой хост это `ErrCrossHostRedirect` (см. § Редиректы) |
| `HTTPCLIENT_SSE_MAX_EVENT_BYTES` | `Config.SSEMaxEventBytes` | `int` | `1048576` | `> 0`, иначе ошибка. Потолок строки и события SSE (`ErrEventTooLarge`) |
| `HTTPCLIENT_MAX_HOSTS` | `Config.MaxHosts` | `int` | `1024` | `>= 1` или `-1` (без предела), `0` и меньше `-1` — ошибка. Сколько разных `req.URL.Host` получают свой breaker и label `host`; следующие — без breaker'а, `host="other"` (см. § Circuit breaker) |
| `HTTPCLIENT_BREAKER_FAILURE_THRESHOLD` | `BreakerConfig.FailureThreshold` | `int` | `5` | `< 0` — ошибка; `0` выключает breaker (осознанный выбор) |
| `HTTPCLIENT_BREAKER_RECOVERY_TIMEOUT` | `BreakerConfig.RecoveryTimeout` | `time.Duration` | `30s` | `< 0` — ошибка; у включённого breaker'а `> 0`. Сколько breaker держится в Open до пробного запроса |

## Retry

Повтор делается только когда он безопасен:

- метод идемпотентен (`GET`, `HEAD`, `OPTIONS`, `PUT`, `DELETE`, `TRACE`), либо
  запрос явно разрешил повтор: `httpclient.AllowRetry(ctx)`, заголовок
  `Idempotency-Key`, либо `HTTPCLIENT_RETRY_NON_IDEMPOTENT=true`;
- тело можно перечитать: `req.GetBody != nil` (`http.NewRequest` ставит его для
  `bytes.Reader`, `strings.Reader`, `bytes.Buffer`). Потоковое тело без `GetBody`
  не ретраится.

Ретраятся статусы `408`, `429`, `502`, `503`, `504` и транспортные ошибки,
включая таймаут попытки (`Timeout`). `500` не ретраится: у большинства
upstream'ов он детерминирован, повтор только жжёт бюджет.

Не ретраятся: отмена или дедлайн ctx вызывающего, ошибка hook'а
`WithBeforeAttempt` и перманентные ошибки транспорта — проверка сертификата
(x509/TLS), неподдерживаемая схема URL, превышенный лимит редиректов.

Пауза — `Retry-After` сервера (секунды или HTTP-дата, максимум 5 минут), иначе
экспоненциальный backoff с jitter; и то и другое под `RetryMaxWait`. Разбор
заголовка экспортирован — `httpclient.ParseRetryAfter(value, now)`: одно правило
RFC 9110 на всех потребителей заголовка. После
исчерпания ретраев возвращается **последний ответ** с его статусом и телом
(`err == nil`): статус разбирает вызывающий.

`WithBeforeAttempt` вызывается перед каждой попыткой, включая первую, с номером
попытки от нуля: место для per-host rate-limiter'а. Ошибка hook'а прерывает
запрос и не считается провалом breaker'а.

## Circuit breaker

Breaker заводится на каждый `req.URL.Host`: отказ одного upstream не размыкает
цепь остальным. Хостов со своим breaker'ом — не больше `HTTPCLIENT_MAX_HOSTS`
(первые встреченные): следующий хост идёт без breaker'а, его метрики — с
`host="other"`, поэтому пользовательские адреса не растят память клиента и число
серий. Провалом считается транспортная ошибка (включая таймаут
попытки, отказ TLS и нарушение redirect-политики) или ответ `502`/`503`/`504`
после ретраев — сигналы «хоста нет». `500`/`501` от живого upstream'а провалом
не считаются, как и отмена ctx вызывающего и ошибка `WithBeforeAttempt`: всплеск
barge-in или ожидания лимитера не должен отключать здоровый хост.

В открытом состоянии `Do` возвращает `httpclient.ErrBreakerOpen` (оборачивает
`gobreaker.ErrOpenState` / `ErrTooManyRequests`) не отправляя запрос.

## Редиректы

Редиректы следуются в пределах `MaxRedirects` и **только в пределах исходного
хоста**: кросс-хостовый редирект — `ErrCrossHostRedirect`. Так API-клиент не
уносит секреты на чужой хост при компрометации DNS или upstream'а.

`HTTPCLIENT_ALLOW_CROSS_HOST_REDIRECTS=true` разрешает переход, но на чужой хост
уезжают только `Accept`, `Accept-Encoding`, `Accept-Language`, `Content-Type`,
`Content-Length`, `Referer`, `User-Agent` и `X-Request-ID` — все остальные
заголовки (в том числе кастомные ключи и подписи) снимаются. `net/http` сам
чистит только `Authorization`, `Cookie`, `WWW-Authenticate` и `Proxy-*`.

## SSE

`Stream(req)` открывает `text/event-stream`. У стрима нет retry и breaker'а:
частично прочитанный поток неретраябелен, а Open-состояние breaker'а рассчитано
на короткие запросы.

- `Timeout` ограничивает **только соединение и ожидание заголовков**
  (`ErrHeaderTimeout`); тело живёт, пока жив `req.Context()` или пока не вызван
  `SSEStream.Close()` — и то и другое завершает фоновую горутину без утечки,
  даже если потребитель перестал читать `Events()` (barge-in).
- Ответ не с `Content-Type: text/event-stream` отвергается (`ErrNotEventStream`),
  неуспешный статус — `*StatusError` с усечённым телом (`errors.As`).
- Строка и событие ограничены `SSEMaxEventBytes`; превышение закрывает поток с
  `ErrEventTooLarge` в `Err()`.
- Парсинг по спеке WHATWG: терминатор строки — CR, LF или CRLF; `data` склеены
  через `\n`; блок без `data` не диспатчится и сбрасывает тип события; `id`
  живёт между событиями (`Event.ID`, `SSEStream.LastEventID()`); `retry`
  доступен как `SSEStream.Retry()` (значение больше `time.Duration` насыщается
  её максимумом) — реконнект httpclient не делает сам.
- Терминатор потока вроде `[DONE]` приходит обычным событием: полезную нагрузку
  интерпретирует вызывающий.

## Observability

Instrumentation-scope `git.stit.tech/stit-core/golibinfra/httpclient`, глобальные
OTel-провайдеры (no-op без `MeterProvider`/`TracerProvider`).

| Сигнал | Тип | Labels | Назначение |
|---|---|---|---|
| `http_client_request_duration_seconds` | Float64 Histogram (`s`) | `host`, `method`, `status`, `mode` | Длительность вызова целиком, включая ретраи и backoff; `mode="sse"` только у `Stream` (у `Do` label отсутствует) |
| `http_client_retry_attempts_total` | Int64 Counter | `host` | Инкремент на каждый состоявшийся повтор (после ожидания backoff) |
| `http_client_breaker_transitions_total` | Int64 Counter | `host`, `from`, `to` | Переходы состояния breaker'а хоста |
| `http_client_breaker_open` | Int64 UpDownCounter | `host` | `1`, пока breaker хоста открыт |
| `httpclient.Do <METHOD>` | OTel span (client) | `http.request.method`, `server.address`, `url.full`, `http.response.status_code` | Спан на вызов: покрывает все попытки и паузы между ними |
| `httpclient.Stream <METHOD>` | OTel span (client) | те же | Спан на весь стрим: закрывается вместе с потоком |
| `HTTP <METHOD>` | OTel span (client) | `http.request.method`, `server.address`, `server.port`, `url.full`, `http.response.status_code` или `error.type`, `http.request.resend_count` у повтора | Дочерний спан попытки и шага редиректа — до заголовков ответа; его W3C trace-context уходит в upstream (глобальный propagator) |

`url.full` — без секретов: userinfo и значения query — `xxxxx`, фрагмента нет, путь — как есть.
Длительность попытки видна в её спане, число повторов — `http_client_retry_attempts_total`.

Значения `status`: код ответа (`200`, `503`), `breaker_open` при отказе открытой
цепи, `error` для транспортных ошибок и отмены ctx. Для `Stream` `status` — код
ответа при чистом завершении потока и `error`, если поток оборвался.
`host` — первые `HTTPCLIENT_MAX_HOSTS` хостов клиента, остальные — `other`;
`method`, `status`, `mode` — конечные множества. Гистограмма использует общие бакеты длительностей
golibinfra (`internal/otelkit`).

## API

```go
func New(cfg Config, opts ...Option) (*Client, error)
func NewConfig() (Config, error)
func NewConfigPrefixed(prefix string) (Config, error)
func (c Config) Validate() error

var Module fx.Option                      // *Client + OnStop → Close
type Options []Option                     // опции в графе: одно значение, порядок слайса
type Params struct{ … }                   // вход Module: Config + опциональные Options

func WithTransport(rt http.RoundTripper) Option     // один на клиент: второй — ErrConfig из New
func WithBeforeAttempt(fn BeforeAttemptFunc) Option // hook'и складываются в порядке опций
type BeforeAttemptFunc func(ctx context.Context, attempt int) error

func AllowRetry(ctx context.Context) context.Context
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool)

func (c *Client) Do(req *http.Request) (*http.Response, error)
func (c *Client) Stream(req *http.Request) (*SSEStream, error)
func (c *Client) Close() error

type Event struct{ ID, Event, Data string }
func (s *SSEStream) Events() <-chan Event
func (s *SSEStream) Err() error
func (s *SSEStream) LastEventID() string
func (s *SSEStream) Retry() time.Duration
func (s *SSEStream) Close() error

type StatusError struct{ Status int; Body string }

var ErrConfig, ErrBreakerOpen, ErrTooManyRedirects, ErrCrossHostRedirect,
    ErrNotEventStream, ErrEventTooLarge, ErrHeaderTimeout error
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
- **Breaker на хост считает 502/503/504 провалом.** Upstream, стабильно
  отвечающий `503`, откроет цепь через `FailureThreshold` вызовов; полностью
  выключить — `HTTPCLIENT_BREAKER_FAILURE_THRESHOLD=0`.
- **Хосты клиента — конечное множество.** Breaker и label `host` есть у первых
  `HTTPCLIENT_MAX_HOSTS` хостов; на webhook'ах по чужим адресам предел быстро
  занят, и основной upstream, встреченный позже, остался бы без breaker'а. Нужен
  исходящий вызов по адресу пользователя — держите для него отдельный `Client`
  (свой `NewConfigPrefixed`, `HTTPCLIENT_BREAKER_FAILURE_THRESHOLD=0`),
  валидируйте хост до вызова и не переиспользуйте клиент основных upstream'ов.
- **Секрет — не в пути.** В спанах и в тексте ошибки (`*url.Error`) userinfo и значения query —
  `xxxxx`, а путь остаётся как есть: токен в пути (вебхуки Slack, Telegram Bot API) уйдёт в
  трейс — передавайте его заголовком или query.
- **`X-Request-ID`** ставится из ctx только если вызывающий не задал свой заголовок.
- **`Close()` не обязателен** при использовании `Module` (вешает `OnStop`); при
  ручной сборке закрывайте клиент сами — он держит собственный пул соединений
  (клон `http.DefaultTransport` со 100 idle-соединениями на хост).
