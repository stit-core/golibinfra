# httpkit — observability

Единый источник правды по сигналам httpkit. Метрики/спаны эмитятся через OTel;
resource attrs (`service.name`, `service.version`, `deployment.environment.name`)
и экспорт настраивает потребитель в своей проводке OTel SDK. Без
зарегистрированного `MeterProvider`/`TracerProvider` инструменты — no-op.

## Метрики

Instrument scope: `git.stit.tech/stit-core/golibinfra/httpkit`. Гистограмма
длительности использует явные bucket-границы (сек): `0.005, 0.01, 0.025, 0.05,
0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30`.

`Metrics` может принять `metric.Meter` явным аргументом; остальные инструменты
(`Recovery`, `MaxBody`, `Idempotency`) читают глобальный `otel.Meter()` — чтобы не
разрастать сигнатуры middleware. Если `Meter` в DI нет, middleware `Metrics`
пропускается, и `http_requests_total`/`http_request_duration_seconds` не эмитятся
(server-span от `OTel` остаётся).

| Метрика | Тип | Labels | Эмитится | Назначение |
|---|---|---|---|---|
| `http_requests_total` | Int64 Counter | `method`, `path`, `status` | `Metrics` middleware (нужен `Meter`) | счётчик запросов; `path` = chi route-pattern (`/users/{id}`), fallback `r.URL.Path` |
| `http_request_duration_seconds` | Float64 Histogram | `method`, `path`, `status` | `Metrics` middleware | длительность обработки в секундах |
| `http_panics_total` | Int64 Counter | `path` | `Recovery` (всегда) | восстановленные panic в цепочке; `path` = `r.URL.Path` |
| `http_max_body_rejections_total` | Int64 Counter | `path` | `MaxBody` (если включён) | запросы, отклонённые по превышению `MaxBytes` (статус 413); `path` = `r.URL.Path` |
| `http_idempotency_total` | Int64 Counter | `path`, `result` | `Idempotency` (если подключён store) | исходы идемпотентности; `result` ∈ {`stored`, `replayed`}; `path` = `r.URL.Path` |

### Заметки по labels

- `Metrics` берёт `path` из chi `RoutePattern()` — низкая кардинальность
  (`/users/{id}`, а не `/users/42`). Прочие middleware используют сырой
  `r.URL.Path`: для panic/rejection/idempotency-путей это приемлемо, так как
  они редки.
- `status` — строка (`strconv.Itoa`). `statusRecorder` перехватывает
  `WriteHeader`; если handler не вызвал явно — дефолт `200`.

## Спаны

`OTel()` оборачивает цепочку в `otelhttp.NewHandler(next, "http")` — server-span
на каждый запрос (имя операции `http`). Trace-context извлекается из входящих
заголовков, что связывает спан с upstream-вызовом (`httpclient`/`grpcclient`
прокидывают propagation). `OTel` ставится всегда, независимо от наличия `Meter`.

## Логи

`AccessLog(log)` пишет строку доступа на каждый запрос (`method`, `path`,
`status`, `dur`); уровень — по статусу: `≥500` → Error, `≥400` → Warn, иначе
Info. `request_id` в строку **не** добавляется явно — `init()` регистрирует
`logkit.RegisterAttr`, и любой ctx-scoped лог-вызов получает атрибут
`request_id` автоматически.

`Recovery(log)` при panic логирует `panic recovered` с полем `stack`
(`debug.Stack()`), затем отвечает `500`.
