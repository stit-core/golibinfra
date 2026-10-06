# Observability anthropickit

Единый источник правды по телеметрии кита. Имена и labels — точно как в коде
(`metrics.go`, `spans.go`, `anthropickit.go`).

## Метрики

Meter: `git.stit.tech/stit-core/golibinfra/anthropickit` (через
`internal/otelkit`). Инструменты создаются в `newInstruments()` при `New`;
ошибка создания инструмента — `panic`.

Запись метрик — в `Client.observe` → `instruments.record(ctx, callResult)`,
вызывается из `Complete`, `CountTokens` и `sseStream.finish()` (однократно:
по исчерпании SSE либо при `Close` до конца стрима).

| Метрика | Тип | Labels | Когда / что |
|---|---|---|---|
| `anthropic_api_calls_total` | counter (`Int64Counter`) | `model`, `op`, `status`, `priced` | +1 на вызов метода кита. Ретраи SDK внутри одного вызова — один инкремент. |
| `anthropic_api_latency_seconds` | histogram (`Float64Histogram`, unit `s`, бакеты `otelkit.LongDurationBuckets()`) | `model`, `op` | Длительность вызова от момента перед запросом до завершения (sync — ответ SDK, stream — исчерпание SSE или `Close`). Включает ретраи. Пишется при любом `status`. |
| `anthropic_api_tokens_total` | counter (`Int64Counter`) | `model`, `kind` | Токены из `Usage`. По одному добавлению на ненулевой `kind`. `op=count_tokens` токены не пишет — подсчёт не тарифицируется. |
| `anthropic_api_cost_micros_total` | counter (`Int64Counter`) | `model` | Стоимость в микро-долларах (`USDMicros`) при `cost > 0`; для модели вне прайс-таблицы серия не появляется. |

Шкала гистограммы — `otelkit.LongDurationBuckets()` (до 600 с) вместо общей
`otelkit.DurationBuckets()` (до 30 с): генерация штатно идёт десятками секунд и
минутами (дефолтный `ANTHROPIC_TIMEOUT` — 10 минут), и на общей шкале весь хвост
лёг бы в `+Inf`, где p95 неотличим от p99. Границы общей шкалы расширенная
продолжает как есть, поэтому сравнение с `_seconds`-метриками других китов на
дашборде сохраняется.

### Значения labels

| Label | Значения |
|---|---|
| `op` | `complete` (`Complete`), `stream` (`Stream`), `count_tokens` (`CountTokens`). |
| `status` | `ok` — успешный ответ / исчерпанный стрим; `error` — ошибка HTTP/транспорта/ctx; `abandoned` — `Close` до конца SSE (в том числе из другой горутины: обрыв чтения, вызванный самим `Close`, сбоем не считается). |
| `priced` | `true`/`false` — есть ли в прайс-таблице клиента та модель, по которой посчитана стоимость: для успешного вызова это модель **из ответа** (та же, что даёт `Response.CostKnown`), для сбоя — запрошенная. `priced="true"` и нулевой `cost_micros_total` означают ровно нулевую стоимость, а не пробел в тарификации. |
| `kind` | `input` (`InputTokens`), `output` (`OutputTokens`), `cache_write` (`CacheWrite5mTokens + CacheWrite1hTokens`), `cache_read` (`CacheReadTokens`), `thinking` (`ThinkingTokens`). |
| `model` | Запрошенная модель, если она есть в прайс-таблице клиента (snapshot-id резолвится в алиас), иначе `other`. |

Нулевые значения токенов не эмитятся (серия для такого `kind` не появляется).
`WebSearchRequests`/`WebFetchRequests` в `anthropic_api_tokens_total` **не**
попадают — они учитываются только в стоимости (`Cost`).

### Токены прерванного вызова

`status=error` и `status=abandoned` пишут накопленный usage и стоимость: API
тарифицирует вход и уже сгенерированный выход, даже если клиент отвалился или
`ctx` отменён. Пусты они только там, где ответа не было вовсе (сбой sync-вызова,
стрим, упавший до `message_start`).

### Семантика label `model`

`model` берётся из `Request.Model`, а при пустом — из `Config.DefaultModel`
(резолвится в `toParams`). Это запрошенная модель, не обязательно тот
идентификатор, что вернул API (`Response.Model`; по нему считается `Cost`).

Кардинальность ограничена прайс-таблицей: модель вне неё схлопывается в
`model="other"`, и кит один раз на каждое такое значение пишет warn-лог с
исходной строкой. Так произвольная модель от клиента гейтвея не разносит
метрики, но остаётся видимой в логах и в `priced="false"`.

Ошибка маппинга запроса до HTTP-вызова (`errStream`, `ErrInvalidRequest` из
`toParams`) и отказ по `ErrMaxTokensRequiresStreaming` метрик не пишут — вызова
API не было.

## Спаны

### Спан кита

`otelkit.Tracer("anthropickit")` открывает один спан на вызов метода
(`SpanKind=CLIENT`), поверх которого ложатся HTTP-спаны попыток SDK:

| Спан | Когда | Атрибуты |
|---|---|---|
| `anthropic.messages` | `Complete` (от вызова до ответа), `Stream` (от `Stream()` до исчерпания SSE или `Close`) | `gen_ai.system=anthropic`, `gen_ai.operation.name=chat`, `gen_ai.request.model`, `gen_ai.request.max_tokens`, `gen_ai.response.model`, `gen_ai.response.id`, `gen_ai.usage.input_tokens` (полный вход с кэшем), `gen_ai.usage.output_tokens`, `gen_ai.response.finish_reasons` |
| `anthropic.count_tokens` | `CountTokens` | `gen_ai.system`, `gen_ai.operation.name=count_tokens`, `gen_ai.request.model`, `gen_ai.usage.input_tokens` |

Ошибка вызова — `RecordError` + `SetStatus(codes.Error)`; refusal ошибкой не
считается и виден только как `gen_ai.response.finish_reasons=["refusal"]`.

**Промпты, ответы и аргументы tool-вызовов в атрибуты не пишутся никогда** —
в спане только метаданные и счётчики токенов.

### Спаны и метрики транспорта

Каждый HTTP-запрос к API идёт через `otelhttp.NewTransport` (SDK получает
`*http.Client` кита). На каждую попытку — клиентский спан `HTTP POST` с
атрибутами otelhttp (`http.request.method`, `url.full`, `server.address`,
`http.response.status_code`), дочерний спану кита; trace-context уходит в
заголовках запроса.

Тот же транспорт эмитит собственные метрики otelhttp — их имена и labels
задаёт `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`, не кит:

| Метрика | Labels |
|---|---|
| `http.client.request.duration` | `server.address`, `server.port`, `url.scheme`, `http.request.method`, `http.response.status_code` |
| `http.client.request.body.size` | те же |

Их бакеты и семантика — вне контроля кита; для «своих» дашбордов используйте
`anthropic_api_*`.

## Логи

`New(cfg, log, ...)` принимает `logkit.Logger` (`nil` → `logkit.Nop()`). Кит пишет:

| Уровень | Сообщение | Когда | Поля |
|---|---|---|---|
| `Warn` | `anthropickit: api error` | API вернул ошибку (любой `Err*` c `*anthropic.Error`) в `Complete`/`Stream`/`CountTokens` | `op`, `model`, `status` (HTTP), `type` (тип ошибки API), `request_id`, `retry_after` (если сервер прислал) |
| `Warn` | `anthropickit: request failed` | транспортная ошибка / таймаут / ошибка SSE-парсинга | `op`, `model`, `error` |
| `Warn` | `anthropickit: model is absent from the price table, metric label collapsed` | первый вызов с моделью вне прайс-таблицы (по одному на модель) | `model`, `label` |
| `Debug` | `anthropickit: request canceled` | `ctx` вызова отменён | `op`, `model` |
| `Debug` | `anthropickit: retrying request` | SDK выполняет повторную попытку (заголовок `X-Stainless-Retry-Count` > 0) | `attempt`, `method`, `path` |

## Health-проба

`anthropickit.Probe(api)` — именованная (`anthropickit`) проба `healthkit.Probe`
с таймаутом 3 с: `Models.Get(Config.DefaultModel)` проверяет ключ, endpoint и
существование модели, ничего не тарифицируя. Проба **не** регистрируется
автоматически: связывать readiness сервиса со сторонним платным API — решение
деплоя. Нужна в `/ready` — добавьте `healthkit.ProvideProbe(anthropickit.Probe)` в
граф.

## Resource attributes

`anthropickit` **не** выставляет resource attrs (`service.name`,
`service.version`, `deployment.environment.name`) — их несёт OTel-resource,
настроенный потребителем в его проводке OTel SDK. Кит лишь читает meter/tracer
из глобальных otel-провайдеров; без зарегистрированного `MeterProvider`/
`TracerProvider` — no-op.
