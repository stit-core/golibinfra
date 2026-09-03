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
`ReadHeaderTimeout`, `WriteTimeout`, `IdleTimeout`); handler — chi router.

## Старт и остановка

`Listen()` занимает порт синхронно и идемпотентно; `Serve()` обслуживает
соединения до `Stop(ctx)` (graceful `Shutdown`). `Module` вызывает `Listen` в
`OnStart` — занятый порт или невалидный адрес валят старт приложения, а не
превращаются в строку лога при уже «запущенном» fx-графе. `Addr()` отдаёт
фактический адрес (для `HTTP_ADDR=:0` в тестах). `Start()` = `Serve()` для
ручной проводки.

## Ленивый BindAPI — порядок имеет значение

`huma.API` создаётся не в `NewServer`, а лениво в `BindAPI()`: `humachi.New`
сразу регистрирует `/openapi.json` и `/docs`, после чего chi запрещает
`router.Use(...)`. Контракт:

1. `NewServer(cfg, schemes...)` — только router + `*http.Server`.
2. `router.Use(middleware...)` — вся net/http-цепочка.
3. `BindAPI()` — создаёт `huma.API`.
4. `huma.Register` / `huma.Get`; `api.UseMiddleware(...)` для huma-уровня.

`BindAPI` идемпотентен, `API()` — алиас. `Module` соблюдает порядок сам.
`SecuritySchemeEntry` регистрирует `components.securitySchemes`; дефолтных схем
httpkit не выставляет.

## Конвейер middleware и порядок

chi применяет middleware в порядке регистрации: первый в списке — внешний,
запрос идёт сверху вниз, ответ — снизу вверх.

| # | Middleware | Условие | Зачем именно здесь |
|---|---|---|---|
| 1 | `RequestID` | всегда | `X-Request-ID` в ctx до всего остального: логи и спан его увидят |
| 2 | `OTel` | всегда | server-span и входящий `traceparent`; всё ниже работает внутри спана, поэтому `trace_id` есть в access-log и panic-log |
| 3 | `Recovery` | всегда | ловит panic из всего, что ниже: 500 + `http_panics_total`; `http.ErrAbortHandler` пробрасывает |
| 4 | `SecurityHeaders` | всегда | заголовки на любой ответ, включая 429 и 413 |
| 5 | `CORS` | если `CORS.Origins` не пуст | preflight отвечает 204 до лимитера и тела |
| 6 | `RateLimit` | если `RateLimit.RPS > 0` | per-IP bucket; дешёвый отказ до тяжёлой работы |
| 7 | `MaxBody` | если `MaxBody.MaxBytes > 0` | `MaxBytesReader` до чтения body handler'ом |
| 8 | `AccessLog` | всегда | строка на запрос с `route`, `status`, `bytes`, `dur` |
| 9 | `Metrics` | всегда | RED на chi route-pattern; уточняет имя спана до `METHOD /route` |
| 10 | `Idempotency` | если в DI есть `IdempotencyStore` | дедуп write-запросов по `Idempotency-Key` |

`HumaAuth` — отдельный слой на `huma.API`.

Почему так: `RequestID` и `OTel` первыми, чтобы контекст запроса был полным у
всех остальных; `Recovery` выше остальных, чтобы panic в любом слое дала
залогированную строку и метрику; `CORS`/`RateLimit` — до дорогой работы;
`Metrics` внутри, чтобы route-pattern уже был известен.

### Зависимости через fx

- `logkit.Logger` — обязателен (`Recovery`, `AccessLog`, `/ready`).
- `IdempotencyStore` (`optional`) — есть в графе → `Idempotency` с
  `Config.Idempotency` (`HTTP_IDEMPOTENCY_*`).
- `AuthExtractor` (`optional`) → `HumaAuth`; нет → auth no-op.
- `[]SecuritySchemeEntry` (группа `httpkit.security_scheme`) → OpenAPI.
- `[]healthkit.Probe` (группа `health`) → `/ready`. Тип именованный:
  `httpkit.Probe` — alias `healthkit.Probe`, а `pgkit.DBProbe`, `natskit.Probe`,
  `cachekit.Probe` возвращают его же. Регистрация — `healthkit.Provide(ctor)`.

## Wrapper ответа

Все middleware оборачивают `http.ResponseWriter` одним типом: он считает статус
и байты, повторный `WriteHeader` игнорирует, реализует `Unwrap()` для
`http.ResponseController`, `Flush()` для SSE/chunked и `Hijack()` для
WebSocket-upgrade. Повторное оборачивание того же writer'а не создаёт новый слой.

## RequestID

Принимает `X-Request-ID` клиента только печатным ASCII без пробелов длиной до
128 байт (id попадает в каждую строку лога), иначе генерирует UUIDv7
(`idkit.NewV7`). Ключ ctx — `internal/requestid`, общий с `httpclient`
(прокидывает заголовок дальше) и `grpckit` (интерсептор пишет тот же ключ).
`init()` регистрирует `logkit.RegisterAttr`: любой ctx-scoped лог-вызов получает
`request_id` автоматически.

## Error-маппинг

`APIError` — problem-объект `{type, title, status, detail, instance, code}`
(`omitempty`), реализует `error` и `huma.StatusError`. Из middleware (до huma)
ошибки пишутся тем же объектом с `Content-Type: application/problem+json`.

- `Error(code, status, detail)`, хелперы `BadRequest`…`Internal`.
- `MapErr(err, mappings...)` — таблица `ErrorMapping{Match, Code, Status, Detail}`.
- `MapIs(err, target, code, status, detail)` — `errors.Is`.
- `MapAs[T](err, code, status, detail)` — `errors.As` по конкретному типу;
  `T = error` запрещён (паника), потому что совпал бы с любой ошибкой.

Неузнанная ошибка возвращается как есть — её подхватит дефолтный
huma-обработчик (500). Доменный код не знает про HTTP; перевод в статус
локализован на границе handler'а.

## Health / readiness

`RegisterHealth(api, log, probes...)`:

- `GET /health` — liveness, всегда `{"status":"ok"}`.
- `GET /ready` — прогоняет пробы; каждая ошибка логируется на `Warn` с индексом
  пробы, ответ — общий `503 unhealthy` без текста ошибок: в них бывают DSN и
  внутренние адреса.
