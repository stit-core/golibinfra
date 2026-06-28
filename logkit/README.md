# logkit

Структурный `Logger` поверх `log/slog` с OTel-bridge'ом, trace-correlation и
PII-redact'ингом. Каждый record уходит в OTel `LoggerProvider` (OTLP HTTP → корп
Loki) и, при `LOG_STDOUT` (вкл по умолчанию), дублируется в JSON-stdout; оба
sink'а автоматически получают `trace_id`/`span_id` из `ctx`. Метрики и трейсы в
stdout не попадают — это только log pipeline.

## Назначение

`logkit` — корп observability-инвариант. Это единая точка логирования для
сервисов stit: структурный JSON-лог, который корп-стек собирает в Loki и
коррелирует с трейсами по `trace_id`.

Чем kit ценен:

- **dual-sink.** Каждая запись идёт в OTLP (`LoggerProvider` → корп Loki) и,
  при `LOG_STDOUT`, в JSON-stdout — видно и в Grafana, и в `kubectl logs` /
  локальной консоли. Метрики и трейсы при этом в stdout не валятся (их пишет
  только OTLP-pipeline в `telemetry`).
- **trace-correlation.** Запись вычитывает trace context из `ctx` и кладёт
  `trace_id`/`span_id` в каждый log record — лог сшивается с трейсом в Grafana.
- **resource attrs из telemetry.** `service.name`/`service.version`/
  `deployment.environment.name` приходят как OTel resource attrs (их ставит
  `telemetry.Init`), а не дублируются в каждый record.
- **`RegisterAttr`.** Глобально регистрируемые поля, которые kit достаёт из `ctx`
  на каждую запись — так `httpkit` middleware пробрасывает `request_id`, не зная
  про конкретный logger.
- **PII-redact.** `NewWithRedact` + `RedactConfig` маскируют чувствительные данные
  (email/телефон/карта) regex-фильтрами до отправки.
- **`log_messages_total{level}`** — счётчик записей по уровню.

В production логировать только через `logkit.Logger` (или `*slog.Logger` из того
же chain — `NewSlog`). `fmt.Print*` и `slog.Default()` в прод-коде запрещены: они
проходят мимо OTLP-bridge'а, trace-correlation, redact'а и метрики.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/logkit"
```

Версионирование и установка модуля — в [корневом README](../README.md)
(§ Версионирование, § Стабильность API). Здесь версию не дублируем.

## Быстрый старт

### Через fx

`Module` provide'ит `logkit.Logger` и `*slog.Logger`, но **не** конфиги — их
регистрирует caller (как и в `pgkit`). Дешёвый способ — `logkit.NewConfig` /
`logkit.NewServiceConfig`, читающие ENV через envconfig. `*sdklog.LoggerProvider`
в граф поставляет `telemetry`.

```go
import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/telemetry"
)

fx.New(
	telemetry.Module,                   // даёт *sdklog.LoggerProvider + resource attrs
	logkit.Module,                      // даёт logkit.Logger и *slog.Logger
	fx.Provide(logkit.NewConfig),       // logkit.Config из ENV
	fx.Invoke(func(log logkit.Logger) {
		log.Info(ctx, "service started")
	}),
)
```

### Логирование с ctx и trace_id

`trace_id`/`span_id` появятся в записи автоматически, если в `ctx` есть активный
span (его кладёт `telemetry`/`httpkit`/`grpckit` через OTel):

```go
func (h *Handler) Do(ctx context.Context, log logkit.Logger) {
	log.Info(ctx, "processing order", "order_id", id, "amount", 1500)
	// → JSON-record с msg, level, time, trace_id, span_id, order_id, amount
}
```

Сигнатуры — `Debug/Info/Warn/Error(ctx, msg string, kv ...any)`; `kv` — пары
ключ-значение в стиле slog. `With(kv ...any)` возвращает `Logger` с
прикреплёнными полями.

### RegisterAttr — проброс полей из ctx

`RegisterAttr` регистрирует функцию, которую kit зовёт на **каждой** записи; если
она вернула `ok == true`, поле добавляется в record. Так инфраструктурный код
(например `httpkit`) пробрасывает `request_id`, не передавая logger явно:

```go
logkit.RegisterAttr(func(ctx context.Context) (string, any, bool) {
	if rid, ok := ctx.Value(requestIDKey{}).(string); ok {
		return "request_id", rid, true
	}
	return "", nil, false
})
```

### NewWithRedact — PII-фильтры

`NewWithRedact` строит `Logger`, который пишет JSON в произвольный `io.Writer` и
маскирует совпадения regex-фильтров. Redact включается флагом `Enabled`:

```go
log := logkit.NewWithRedact(
	logkit.Config{Level: "info"},
	logkit.RedactConfig{Enabled: true},  // дефолтные паттерны: email/телефон/карта
	os.Stdout,
)
log.Info(ctx, "user login", "email", "john.doe@example.com")
// → ..."email":"[REDACTED]"...
```

## Конфигурация

Все три конфиг-структуры читаются через `envconfig`. Имена полей и envconfig-теги —
**part of public API**: менять их = breaking change (корневой README
§ Стабильность API; для logkit это `Config` и `RedactConfig`).

### `Config`

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `LOG_LEVEL` | `Level` | `string` | `info` | Минимальный уровень записи (`debug`/`info`/`warn`/`error`). Парсится `slog.Level.UnmarshalText`; невалидное значение → `info`. |
| `LOG_STDOUT` | `Stdout` | `bool` | `true` | Дублировать записи в JSON-stdout (вдобавок к OTLP). `false` — только OTLP. Default действует через `envconfig`; в struct-литерале zero-value `false` — задавайте поле явно. |

### `ServiceConfig`

Источник идентичности сервиса. На прод-пути значения уходят в OTel resource attrs
через `telemetry`, поэтому в каждый log record kit их **не** дублирует (см.
§ Observability).

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `OTEL_SERVICE_NAME` | `Service` | `string` | *(пусто)* | Имя сервиса. |
| `APP_ENV` | `Env` | `string` | `dev` | Окружение (`prod`/`staging`/`dev`/…). |
| `OTEL_SERVICE_VERSION` | `Version` | `string` | `dev` | Версия сервиса (semver / git short-sha). |

### `RedactConfig`

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `LOG_REDACT_ENABLED` | `Enabled` | `bool` | `false` | Включает redact-слой. Выключен — записи идут без маскирования. |
| `LOG_REDACT_PATTERNS` | `Patterns` | `[]string` | *(пусто → дефолтные паттерны)* | Regex-фильтры PII. Пустой список → встроенные паттерны email/телефон/карта. Невалидный regex молча пропускается. |
| *(нет тега)* | `ReplaceWith` | `string` | `[REDACTED]` | Строка-замена для совпадений. |

Дефолтные паттерны (применяются, когда `Patterns` пуст): email, телефон
(`+?` 10–15 цифр), номер карты (4×4 цифры с опциональным разделителем).

## Observability

Единый источник правды о том, что эмитит kit, — этот раздел (он сходится с
корневым README § Что эмитит).

### Метрика

| Метрика | Тип | Labels | Когда |
|---|---|---|---|
| `log_messages_total` | counter | `level` (`debug`/`info`/`warn`/`error`) | Инкремент на каждом вызове `Debug`/`Info`/`Warn`/`Error`. |

Counter регистрируется через `otel.Meter("git.stit.tech/stit-core/golibinfra/logkit")`.
Если глобальный meter ещё не инициализирован (нет OTel SDK) — инструмент `nil`, и
запись метрики становится no-op; сам лог при этом пишется как обычно.

### trace_id / span_id

В OTLP-sink (`New`/`NewSlog`, через `otelslog`-bridge) `trace_id`/`span_id`
embedding'ит сам `otelslog` — он читает span context из `ctx`. В stdout-sink
(raw `slog.JSONHandler`) и на test-пути (`NewWithWriter`/`NewWithRedact`) их
добавляет внутренний `traceHandler`. Поля появляются только при валидном span
context в `ctx`.

### Resource attrs (service / env / version)

`service.name` / `service.version` / `deployment.environment.name` — это OTel
**resource attrs**, их выставляет `telemetry.Init` из `telemetry.Config`. В сам
log record kit их **не** кладёт: иначе они бы шли дважды в каждый OTLP-record.
Следствие: stdout-записи (raw JSON-handler resource attrs не получает) полей
`service`/`env`/`version` не содержат — идентичность сервиса там определяется
средой запуска (pod/namespace). `ServiceConfig` + `registerServiceAttrs`
существуют для test-сценариев и модулем не вызываются.

Спаны kit не эмитит.

## API

### Logger

```go
type Logger interface {
	Debug(ctx context.Context, msg string, kv ...any)
	Info(ctx context.Context, msg string, kv ...any)
	Warn(ctx context.Context, msg string, kv ...any)
	Error(ctx context.Context, msg string, kv ...any)
	With(kv ...any) Logger
}
```

Конструкторы:

```go
// Прод-путь: fan-out в OTel LoggerProvider (OTLP → Loki) + JSON-stdout (LOG_STDOUT).
func New(cfg Config, lp *sdklog.LoggerProvider) Logger
func NewSlog(cfg Config, lp *sdklog.LoggerProvider) *slog.Logger

// PII-redact: JSON в произвольный writer, redact по RedactConfig.
func NewWithRedact(cfg Config, redactCfg RedactConfig, w io.Writer) Logger

// Test-only: JSON в writer без OTel SDK.
func NewWithWriter(cfg Config, w io.Writer) Logger
func NewSlogWithWriter(cfg Config, w io.Writer) *slog.Logger

// No-op Logger (тесты, заглушки).
func Nop() Logger
```

`NewSlog` отдаёт `*slog.Logger` с тем же handler-chain, что и `New` — для
consumer'ов, которые принимают `*slog.Logger` напрямую (`timerkit` и пр.). Это
единое место конфигурации chain'а.

### RegisterAttr

```go
type Attr func(ctx context.Context) (key string, value any, ok bool)

func RegisterAttr(fn Attr)
```

Регистрирует глобальную функцию-источник поля. Вызывается на каждой записи; при
`ok == true` пара `key`/`value` добавляется в record. Реестр потокобезопасен.
Регистрация глобальна и аддитивна — типично вызывается один раз при старте
сервиса (инфраструктурным кодом вроде `httpkit`).

### NewWithRedact / RedactConfig

```go
type RedactConfig struct {
	Patterns    []string  // LOG_REDACT_PATTERNS
	ReplaceWith string    // default "[REDACTED]"
	Enabled     bool      // LOG_REDACT_ENABLED
}

func NewWithRedact(cfg Config, redactCfg RedactConfig, w io.Writer) Logger
func NewRedactHandler(inner slog.Handler, cfg RedactConfig) slog.Handler
```

`NewRedactHandler` оборачивает любой `slog.Handler`: сериализует record в JSON,
прогоняет через regex-замены и передаёт результат во `inner`. `NewWithRedact` —
готовая сборка handler'а поверх JSON-writer'а с trace-полями.

### fx Module

```go
var Module = fx.Module("logkit", ...) // provides New, NewSlog

func NewConfig() (Config, error)         // envconfig.Process для Config
func NewServiceConfig() (ServiceConfig, error)  // envconfig.Process для ServiceConfig
```

`Module` не provide'ит конфиги и `*sdklog.LoggerProvider` — их регистрирует caller
(provider'ы конфигов — `NewConfig`/`NewServiceConfig`; `LoggerProvider` — из
`telemetry`).

## Тестирование

Для unit-тестов LoggerProvider избыточен — используйте writer-конструкторы и
парсите JSON-вывод:

```go
var buf bytes.Buffer
log := logkit.NewWithWriter(logkit.Config{Level: "debug"}, &buf)
log.Info(ctx, "hello", "k", "v")

var m map[string]any
_ = json.Unmarshal(buf.Bytes(), &m)
// m["msg"], m["level"], m["time"], m["k"], при span в ctx — m["trace_id"]/m["span_id"]
```

Проверка redact:

```go
log := logkit.NewWithRedact(
	logkit.Config{Level: "debug"},
	logkit.RedactConfig{Enabled: true},
	&buf,
)
log.Info(ctx, "user login", "email", "john.doe@example.com")
// buf не содержит "john.doe@example.com", содержит "[REDACTED]"
```

`Nop()` — заглушка, не паникует и ничего не пишет.

## Инварианты и подводные камни

- **В production только `logkit.Logger`.** `fmt.Print*` и `slog.Default()` в
  прод-коде запрещены — они обходят OTLP-bridge, trace-correlation, PII-redact и
  метрику `log_messages_total`. Где нужен `*slog.Logger` (сторонний consumer) —
  берите его из `NewSlog`, а не из `slog.Default()`.

- **resource attrs не дублировать в record.** `service`/`env`/`version`
  приходят как OTel resource attrs от `telemetry`. Не регистрируйте их через
  `RegisterAttr` на прод-пути — поедут дважды в каждый OTLP-record.

- **PII-redact выключен по умолчанию.** `RedactConfig.Enabled == false` — записи
  идут без маскирования. Включайте на путях, где в логи могут попасть PII. Redact
  работает по тексту сериализованного record'а: маскируется всё, что совпало с
  паттерном, независимо от ключа. Невалидный regex в `Patterns` молча
  пропускается — проверяйте свои паттерны.

- **`RegisterAttr` глобален и аддитивен.** Реестр — пакетный, регистрация
  складывается. Зовите при старте; повторные регистрации того же поля добавят его
  несколько раз.

- **`log_messages_total` no-op без meter.** Без инициализированного OTel SDK
  counter не создаётся, инкремент превращается в no-op; лог при этом пишется.

- **Loki не парсить через `| json`.** OTel→Loki экспортер уже разложил
  resource/log attrs в stream labels — повторный `| json` лишний. Фильтр по
  уровню — `detected_level=~"error"` (а не по полю из тела). См. корневой README
  § Anti-patterns.
