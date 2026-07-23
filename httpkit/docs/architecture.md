# httpkit — архитектура

## Два слоя: chi + huma

`Server` несёт оба слоя:

- **`*chi.Mux`** (`Router()`) — транспорт: маршрутизация, точка крепления
  net/http-middleware (`router.Use`).
- **`huma.API`** (`API()`/`BindAPI()`) — типобезопасные операции с
  авто-генерацией OpenAPI и валидацией ввода/вывода. Поверх chi через
  `humachi.New`.

Бизнес-роуты регистрируются на `huma.API` (`huma.Get`/`huma.Register`),
инфраструктурные middleware — на `*chi.Mux`. Auth-middleware живёт на уровне
huma (`api.UseMiddleware`), потому что ему нужен доступ к `Operation().Security`.

`*http.Server` собирается в `NewServer` из `Config` (`Addr`, `ReadTimeout`,
`WriteTimeout`, `IdleTimeout`); handler — chi router. `Start` запускает
`ListenAndServe` (игнорируя `http.ErrServerClosed`), `Stop` — graceful
`Shutdown`.

## Ленивый BindAPI — порядок имеет значение

`huma.API` создаётся **не** в `NewServer`, а лениво в `BindAPI()`.

`humachi.New` (huma ≥ 2.37) при создании сразу регистрирует route'ы
`/openapi.json` и `/docs` на chi router. chi запрещает `router.Use(...)` после
того, как зарегистрирован хоть один route (panic «all middlewares must be
defined before routes on a mux»). Поэтому контракт жёсткий:

1. `NewServer(cfg, schemes...)` — только router + `*http.Server`, без huma.API.
2. `router.Use(middleware...)` — вся net/http-цепочка.
3. `BindAPI()` — создаёт `huma.API` (и регистрирует `/openapi.json`, `/docs`).
4. `huma.Register` / `huma.Get` для бизнес-роутов; `api.UseMiddleware(...)` для
   huma-уровневых middleware.

`BindAPI` идемпотентен: повторный вызов возвращает тот же `huma.API`. `API()` —
алиас на `BindAPI()`. `Module` соблюдает этот порядок автоматически.

`SecuritySchemeEntry` (имя + `*huma.SecurityScheme`) регистрируют
`components.securitySchemes` в OpenAPI. httpkit **не** выставляет дефолтных
схем — caller передаёт их аргументами `NewServer` или через fx-группу
`httpkit.security_scheme`.

## Конвейер middleware и порядок

`Module` собирает net/http-цепочку в фиксированном порядке. chi применяет
middleware в порядке регистрации — первый в списке оказывается **внешним**
(ближе к сети), запрос проходит сверху вниз, ответ — снизу вверх.

| # | Middleware | Условие | Зачем именно здесь |
|---|---|---|---|
| 1 | `CORS` | если `CORS.Origins` не пуст | preflight `OPTIONS` отвечает 204 до всего остального; no-op при пустых origins |
| 2 | `RateLimit` | если `RateLimit.RPS != 0` | отсекает поток до тяжёлой работы; per-IP token-bucket |
| 3 | `MaxBody` | если `MaxBody.MaxBytes != 0` | оборачивает `r.Body` в `MaxBytesReader` до чтения handler'ом |
| 4 | `RequestID` | всегда | генерирует/принимает `X-Request-ID`, кладёт в ctx — чтобы дальше его видели логи |
| 5 | `Recovery` | всегда | ловит panic из всего, что ниже; логирует + 500 + `http_panics_total` |
| 6 | `SecurityHeaders` | всегда | выставляет security-заголовки на ответ |
| 7 | `AccessLog` | всегда | пишет строку доступа (после `RequestID` — чтобы `request_id` был в логе) |
| 8 | `Metrics` | если в DI есть `metric.Meter` | RED-метрики на chi route-pattern |
| 9 | `OTel` | всегда | `otelhttp` server-span (внутренний слой) |
| 10 | `Idempotency` | если в DI есть `IdempotencyStore` | дедуп write-запросов по `Idempotency-Key` |

`HumaAuth` — отдельный слой: регистрируется на `huma.API`
(`api.UseMiddleware`), а не на chi.

### Почему именно такой порядок

- `RequestID` **перед** `AccessLog` и `Recovery`: оба используют ctx с
  request-id (через зарегистрированный `logkit.RegisterAttr`).
- `Recovery` **выше** `AccessLog`/`Metrics`/`OTel`: panic, поднявшаяся из
  handler'а, всё равно даст залогированную строку (recover вернёт управление, и
  внешние middleware допишут ответ/метрику).
- `CORS`/`RateLimit` — **самые внешние**: дешёвый отказ (preflight 204, 429) не
  должен тратить downstream-ресурсы.
- `OTel` — **внутренний** относительно `Metrics`: span охватывает только сам
  handler, не накладные расходы цепочки.

### Зависимости через fx (optional)

- `metric.Meter` (`optional:"true"`) — если потребитель подключил `metric.Meter`
  в граф → добавляется `Metrics` с этим meter'ом. Нет — прочие instrument'ы берут
  глобальный `otel.Meter` (no-op без `MeterProvider`), спаны идут через `OTel`.
- `IdempotencyStore` (`optional:"true"`) — есть в графе (напр.
  `cachekit` provide'ит Redis-store) → добавляется `Idempotency` с дефолтным
  `IdempotencyConfig` (`Header: "Idempotency-Key"`, `TTL: 24h`,
  `MaxBodyBytes: 65536`).
- `AuthExtractor` (`optional:"true"`) — передаётся в `HumaAuth`. Нет → auth
  no-op (next без проверки).
- `[]SecuritySchemeEntry` (группа `httpkit.security_scheme`) → OpenAPI
  securitySchemes.
- `[]Probe` (группа `health`) → readiness-проверки в `/ready`.

## RequestID и кросс-транспортный ключ

`RequestID` читает `X-Request-ID` из запроса (или генерирует UUID), пишет его в
заголовок ответа и кладёт в ctx через `ContextWithRequestID` (общий
unexported-ключ). `init()` регистрирует `logkit.RegisterAttr` так, что любой
ctx-scoped лог-вызов автоматически получает атрибут `request_id` — дублировать
его в `AccessLog` не нужно.

Ключ общий с `grpckit`: HTTP-middleware и gRPC-интерсептор пишут request-id под
один ctx-ключ, поэтому `RequestIDFrom(ctx)` работает независимо от транспорта.

## Error-маппинг

`APIError` — единая модель ошибки в формате RFC 7807-подобного problem-объекта:
`{type, title, status, detail, instance, code}` (все поля `omitempty`).
Реализует `error` и `huma.StatusError` (`GetStatus()`), поэтому huma сериализует
его с корректным HTTP-статусом.

- `Error(code, status, detail)` — базовый конструктор; `Title` берётся из
  `http.StatusText(status)`.
- Хелперы по статусу: `BadRequest`, `Unauthorized`, `Forbidden`, `NotFound`,
  `Conflict`, `Internal` — `(code, detail)`.
- `WithDefaults()` — проставляет `Type="about:blank"` и `Title` из статуса, если
  пусты (явный `Type` не перетирается).

Перевод доменных ошибок в `APIError` — на границе handler'а:

- `MapErr(err, mappings...)` — таблица правил `ErrorMapping{Match, Code, Status,
  Detail}`; первое совпадение по `Match(err)` → `APIError`. Нет совпадений →
  исходная ошибка возвращается как есть.
- `MapSentinel[E error](err, target, code, status, detail)` — generic: матчит
  `errors.Is(err, target)` **или** `errors.As(err, &typedE)`. Удобно для пары
  «sentinel + типизированная ошибка одного домена».

Решение-WHY: доменный код возвращает свои ошибки (`pgkit.ErrNotFound`,
sentinel'ы) и **не** знает про HTTP; перевод в статус/код локализован в одном
месте transport-слоя. `MapErr`/`MapSentinel` возвращают `error` (не паникуют),
оставляя неузнанные ошибки нетронутыми — их подхватит дефолтный huma-обработчик
(500).

## Health / readiness

`RegisterHealth(api, probes...)` (вызывается из `Module` через `registerHealth`)
регистрирует:

- `GET /health` — liveness, всегда `{"status":"ok"}`, `Security: []` (без auth).
- `GET /ready` — readiness: прогоняет все `Probe` (`func(ctx) error`); любая
  ошибка → `503` (`huma.Error503ServiceUnavailable` со всеми ошибками). Probes
  приходят через fx-группу `health` (напр. `pgkit.DBProbe`).
