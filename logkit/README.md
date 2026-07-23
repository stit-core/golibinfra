# logkit

Структурный `Logger` поверх `log/slog` с trace-correlation и PII-redact'ингом.
Каждый record сериализуется в JSON и пишется в stdout (при `LOG_STDOUT`, вкл по
умолчанию) — либо в произвольный `io.Writer` через `NewWithWriter` (file-sink на
стороне потребителя). `trace_id`/`span_id` подхватываются из `ctx`
автоматически.

## Назначение

`logkit` — единая точка логирования для сервисов stit: структурный JSON-лог с
консистентным набором полей, который собирает окружение запуска (`docker logs` /
файловый сборщик).

Чем kit ценен:

- **structured JSON в stdout.** Каждая запись — JSON-объект с `msg`, `level`,
  `time` и переданными парами ключ-значение; при `LOG_STDOUT` пишется в stdout,
  при `false` — отбрасывается (`io.Discard`). Для файла/иного sink'а —
  `NewWithWriter(cfg, w)`.
- **trace-correlation.** Запись вычитывает trace context из `ctx` и кладёт
  `trace_id`/`span_id` в каждый log record (если в `ctx` есть активный span).
- **`RegisterAttr`.** Глобально регистрируемые поля, которые kit достаёт из `ctx`
  на каждую запись — так `httpkit` middleware пробрасывает `request_id`, не зная
  про конкретный logger.
- **PII-redact.** `NewWithRedact` + `RedactConfig` маскируют чувствительные данные
  (email/телефон/карта) regex-фильтрами до записи.
- **`log_messages_total{level}`** — счётчик записей по уровню (эмитится через
  глобальный `otel.Meter`; без зарегистрированного `MeterProvider` — no-op, сам
  лог при этом пишется как обычно).

В production логировать только через `logkit.Logger` (или `*slog.Logger` из того
же chain — `NewSlog`). `fmt.Print*` и `slog.Default()` в прод-коде запрещены: они
проходят мимо trace-correlation, redact'а, метрики и единого формата.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/logkit"
```

Версионирование и установка модуля — в [корневом README](../README.md)
(§ Версионирование, § Стабильность API). Здесь версию не дублируем.

## Быстрый старт

### Через fx

`Module` provide'ит `logkit.Logger` и `*slog.Logger`, но **не** конфиги — их
регистрирует caller (как и в `pgkit`). Дешёвый способ — `logkit.NewConfig`,
читающий ENV через envconfig.

```go
import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

fx.New(
	logkit.Module,                      // даёт logkit.Logger и *slog.Logger
	fx.Provide(logkit.NewConfig),       // logkit.Config из ENV
	fx.Invoke(func(log logkit.Logger) {
		log.Info(ctx, "service started")
	}),
)
```

### Логирование с ctx и trace_id

`trace_id`/`span_id` появятся в записи автоматически, если в `ctx` есть активный
span:

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

Конфиг-структуры читаются через `envconfig`. Имена полей и envconfig-теги —
**part of public API**: менять их = breaking change (корневой README
§ Стабильность API; для logkit это `Config` и `RedactConfig`).

### `Config`

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `LOG_LEVEL` | `Level` | `string` | `info` | Минимальный уровень записи (`debug`/`info`/`warn`/`error`). Парсится `slog.Level.UnmarshalText`; невалидное значение → `info`. |
| `LOG_STDOUT` | `Stdout` | `bool` | `true` | Писать записи в JSON-stdout. `false` — записи отбрасываются (`io.Discard`). Default действует через `envconfig`; в struct-литерале zero-value `false` — задавайте поле явно. |

Для записи в файл или иной sink используйте `NewWithWriter(cfg, w)` с нужным
`io.Writer` (ротацию/файл предоставляет потребитель).

### `RedactConfig`

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `LOG_REDACT_ENABLED` | `Enabled` | `bool` | `false` | Включает redact-слой. Выключен — записи идут без маскирования. |
| `LOG_REDACT_PATTERNS` | `Patterns` | `[]string` | *(пусто → дефолтные паттерны)* | Regex-фильтры PII. Пустой список → встроенные паттерны email/телефон/карта. Невалидный regex молча пропускается. |
| *(нет тега)* | `ReplaceWith` | `string` | `[REDACTED]` | Строка-замена для совпадений. |

Дефолтные паттерны (применяются, когда `Patterns` пуст): email, телефон
(`+?` 10–15 цифр), номер карты (4×4 цифры с опциональным разделителем).

## Observability

`log_messages_total` (counter, label `level`) регистрируется через
`otel.Meter("git.stit.tech/stit-core/golibinfra/logkit")` и инкрементируется на
каждом `Debug`/`Info`/`Warn`/`Error`. Если глобальный `MeterProvider` не
установлен — инструмент `nil`, запись метрики становится no-op; сам лог при этом
пишется как обычно. `trace_id`/`span_id` добавляются в record при валидном span
context в `ctx`. Спаны kit не эмитит.

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
// Прод-путь: JSON в stdout (LOG_STDOUT), иначе io.Discard.
func New(cfg Config) Logger
func NewSlog(cfg Config) *slog.Logger

// В произвольный writer (file-sink, тесты).
func NewWithWriter(cfg Config, w io.Writer) Logger
func NewSlogWithWriter(cfg Config, w io.Writer) *slog.Logger

// PII-redact: JSON в произвольный writer, redact по RedactConfig.
func NewWithRedact(cfg Config, redactCfg RedactConfig, w io.Writer) Logger

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

`Module` не provide'ит конфиги — их регистрирует caller
(`NewConfig`/`NewServiceConfig`).

## Тестирование

Для unit-тестов используйте writer-конструкторы и парсите JSON-вывод:

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
  прод-коде запрещены — они обходят trace-correlation, PII-redact, метрику
  `log_messages_total` и единый формат. Где нужен `*slog.Logger` (сторонний
  consumer) — берите его из `NewSlog`, а не из `slog.Default()`.

- **PII-redact выключен по умолчанию.** `RedactConfig.Enabled == false` — записи
  идут без маскирования. Включайте на путях, где в логи могут попасть PII. Redact
  работает по тексту сериализованного record'а: маскируется всё, что совпало с
  паттерном, независимо от ключа. Невалидный regex в `Patterns` молча
  пропускается — проверяйте свои паттерны.

- **`RegisterAttr` глобален и аддитивен.** Реестр — пакетный, регистрация
  складывается. Зовите при старте; повторные регистрации того же поля добавят его
  несколько раз.

- **`log_messages_total` no-op без meter.** Без установленного `MeterProvider`
  counter не создаётся, инкремент превращается в no-op; лог при этом пишется.
