# logkit

Структурный `Logger` поверх `log/slog`: JSON-записи в stdout (`LOG_STDOUT`) или
в произвольный `io.Writer`, опциональный второй sink в OTel `LoggerProvider`
(OTLP), PII-redact на уровне атрибутов, `trace_id`/`span_id` из `ctx`,
`RegisterAttr` и метрика `log_messages_total{level}`.

## Назначение

`logkit` — единая точка логирования сервисов stit. Одна цепочка handler'ов,
один формат записи, две точки выхода:

- **JSON в stdout** — то, что собирает `docker logs` / файловый сборщик. При
  `LOG_STDOUT=false` JSON-sink отбрасывает записи (`io.Discard`); произвольный
  writer — через `NewWithWriter`.
- **OTel Logs (OTLP)** — второй sink, включается опцией `WithOTel` или
  автоматически через fx, если в графе есть `log.LoggerProvider`
  (его отдаёт `telemetry.Module`). Уровень, `msg`, атрибуты и группы маппятся в
  `log.Record`; `trace_id`/`span_id` SDK берёт из `ctx` и кладёт в поля записи.
- **trace-correlation** — при валидном span context в `ctx` JSON-запись
  получает `trace_id`/`span_id`.
- **`RegisterAttr`** — глобально регистрируемые поля из `ctx` (так `httpkit`
  пробрасывает `request_id`); попадают в оба sink'а.
- **PII-redact** — `WithRedact` + `RedactConfig`: regex-маски применяются к
  `msg` и строковым значениям атрибутов, включая `With()`-attrs, группы,
  `error` и JSON-представление `Any`-значений; числа, `bool`, `time`, `duration`
  не трогаются. Работает до fan-out — маскируются оба sink'а.
- **Fail-fast** — невалидный `LOG_LEVEL` или regex в `LOG_REDACT_PATTERNS`
  даёт ошибку конструктора (в fx — отказ сборки графа).

В production логировать только через `logkit.Logger` (или `*slog.Logger` из
той же цепочки — `NewSlog`). `fmt.Print*` и `slog.Default()` в прод-коде
запрещены: они проходят мимо trace-correlation, redact'а, метрики и единого
формата.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/logkit"
```

Версионирование и установка модуля — в [корневом README](../README.md)
(§ Версионирование, § Стабильность API).

## Быстрый старт

### Через fx

`Module` provide'ит `logkit.Logger` и `*slog.Logger`, но **не** конфиги — их
регистрирует caller. `RedactConfig` и `log.LoggerProvider` — опциональные
зависимости: нет в графе → redact выключен, OTel-sink не добавляется.

```go
fx.New(
	fx.Provide(logkit.NewConfig),        // Config из ENV (LOG_*)
	fx.Provide(logkit.NewRedactConfig),  // опционально: RedactConfig из ENV
	telemetry.Module,                    // опционально: даёт log.LoggerProvider → OTLP-sink
	logkit.Module,                       // → logkit.Logger, *slog.Logger
	fx.Invoke(func(log logkit.Logger) {
		log.Info(ctx, "service started")
	}),
)
```

### Без fx

Конструкторы принимают опции; все возвращают ошибку конфигурации:

```go
log, err := logkit.New(cfg)                                   // stdout по cfg.Stdout
log, err := logkit.NewWithWriter(cfg, os.Stdout)              // явный writer
log, err := logkit.NewWithWriter(cfg, w,
	logkit.WithRedact(redactCfg),                             // PII-маски
	logkit.WithOTel(loggerProvider),                          // второй sink
	logkit.WithService(svcCfg),                               // service/env/version в каждой записи
)
```

### Логирование с ctx и trace_id

```go
func (h *Handler) Do(ctx context.Context, log logkit.Logger) {
	log.Info(ctx, "processing order", "order_id", id, "amount", 1500)
	// → {"time":…,"level":"INFO","msg":"processing order","order_id":…,"amount":1500,"trace_id":…,"span_id":…}
}
```

Сигнатуры — `Debug/Info/Warn/Error(ctx, msg string, kv ...any)`; `kv` — пары
ключ-значение в стиле slog. `With(kv ...any)` возвращает `Logger` с
прикреплёнными полями.

### RegisterAttr — проброс полей из ctx

`RegisterAttr` регистрирует функцию, которую kit зовёт на **каждой** записи;
при `ok == true` поле добавляется в record (в оба sink'а):

```go
logkit.RegisterAttr(func(ctx context.Context) (string, any, bool) {
	if rid, ok := ctx.Value(requestIDKey{}).(string); ok {
		return "request_id", rid, true
	}
	return "", nil, false
})
```

### Redact

```go
log, err := logkit.NewWithWriter(cfg, os.Stdout, logkit.WithRedact(logkit.RedactConfig{Enabled: true}))
log.With("owner", "owner@example.com").Info(ctx, "login john@example.com", "phone", "+79161234567", "elapsed", 1500*time.Millisecond)
// → "msg":"login [REDACTED]", "owner":"[REDACTED]", "phone":"[REDACTED]", "elapsed":1500000000
```

## Конфигурация

Конфиг-структуры читаются через `envconfig`. Имена полей и envconfig-теги —
**part of public API** (корневой README § Стабильность API).

### `Config`

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `LOG_LEVEL` | `Level` | `string` | `info` | Минимальный уровень (`debug`/`info`/`warn`/`error`, регистр не важен; пустая строка = `info`). Невалидное значение → ошибка `NewConfig`/конструктора (`ErrConfig`). |
| `LOG_STDOUT` | `Stdout` | `bool` | `true` | Писать JSON в stdout. `false` — JSON-sink отбрасывает записи (OTel-sink, если подключён, работает). Default действует через `envconfig`; в struct-литерале zero-value `false` — задавайте поле явно. |

### `RedactConfig`

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `LOG_REDACT_ENABLED` | `Enabled` | `bool` | `false` | Включает redact-слой. |
| `LOG_REDACT_PATTERNS` | `Patterns` | `Patterns` (`[]string`) | *(пусто → дефолтные)* | Regex-фильтры; в ENV разделитель `;` (запятая встречается в квантификаторах `{10,15}`). Невалидный regex → ошибка конструктора (`ErrConfig`). |
| `LOG_REDACT_REPLACE_WITH` | `ReplaceWith` | `string` | `[REDACTED]` | Строка-замена. |

Дефолтные паттерны (при пустом `Patterns`) — эвристики компактных форм:
email; телефон — E.164 с `+` (10–15 цифр) либо RU-номер `7`/`8` + 10 цифр;
карта — 4×4 цифры с опциональным разделителем. Десятизначные идентификаторы
без `+`/ведущей `7`/`8` не маскируются. Свой набор задаётся целиком — дефолтные
при этом не применяются.

### `ServiceConfig` (`WithService`)

| Env | Поле | Default | Назначение |
|---|---|---|---|
| `OTEL_SERVICE_NAME` | `Service` | — | поле `service` |
| `APP_ENV` | `Env` | `dev` | поле `env` |
| `OTEL_SERVICE_VERSION` | `Version` | `dev` | поле `version` |

Опция `WithService` добавляет непустые поля в каждую запись — для sink'а без
OTel-resource (файловый сборщик). `Module` эту опцию не применяет: в OTLP
идентичность сервиса несёт resource из `telemetry`.

## Observability

- **`log_messages_total`** (counter, label `level`) — через
  `otel.Meter("git.stit.tech/stit-core/golibinfra/logkit")`, инкремент на каждом
  `Debug`/`Info`/`Warn`/`Error` (включая отфильтрованные по уровню). Без
  `MeterProvider` — no-op.
- **OTel-sink** — `log.Record` с `Timestamp`, `Severity` (Debug 5 / Info 9 /
  Warn 13 / Error 17), `SeverityText`, `Body = msg`, атрибутами (группы —
  вложенные map-значения; `duration` — int64 ns; `time` — RFC3339Nano; `error`
  — строка; прочие `Any` — JSON-строка) и trace/span id из `ctx`.
  Instrumentation scope — `git.stit.tech/stit-core/golibinfra/logkit`. Уровень
  фильтрации тот же `LOG_LEVEL`.
- Спаны kit не эмитит.

## API

```go
type Logger interface {
	Debug(ctx context.Context, msg string, kv ...any)
	Info(ctx context.Context, msg string, kv ...any)
	Warn(ctx context.Context, msg string, kv ...any)
	Error(ctx context.Context, msg string, kv ...any)
	With(kv ...any) Logger
}

func New(cfg Config, opts ...Option) (Logger, error)                        // stdout при cfg.Stdout, иначе io.Discard
func NewSlog(cfg Config, opts ...Option) (*slog.Logger, error)
func NewWithWriter(cfg Config, w io.Writer, opts ...Option) (Logger, error)  // без gating по cfg.Stdout
func NewSlogWithWriter(cfg Config, w io.Writer, opts ...Option) (*slog.Logger, error)
func Nop() Logger

func WithRedact(cfg RedactConfig) Option
func WithOTel(lp log.LoggerProvider) Option
func WithService(cfg ServiceConfig) Option

func NewRedactHandler(inner slog.Handler, cfg RedactConfig) (slog.Handler, error) // обёртка над любым slog.Handler
func NewOTelHandler(lp log.LoggerProvider, level slog.Leveler) slog.Handler       // мост slog → OTel Logs

type Attr func(ctx context.Context) (key string, value any, ok bool)
func RegisterAttr(fn Attr)

func NewConfig() (Config, error)               // envconfig + Validate
func NewRedactConfig() (RedactConfig, error)
func NewServiceConfig() (ServiceConfig, error)
func (c Config) Validate() error

var ErrConfig error                            // невалидный уровень или regex
var Module fx.Option                           // provides *slog.Logger, Logger; optional: RedactConfig, log.LoggerProvider
```

Цепочка handler'ов: redact → `RegisterAttr`-атрибуты → fan-out (JSON с
`trace_id`/`span_id`; OTel). `NewSlog` отдаёт `*slog.Logger` с той же цепочкой
— для сторонних библиотек, принимающих `*slog.Logger`.

## Тестирование

Writer-конструкторы и разбор JSON:

```go
var buf bytes.Buffer
log, _ := logkit.NewWithWriter(logkit.Config{Level: "debug"}, &buf)
log.Info(ctx, "hello", "k", "v")

var m map[string]any
_ = json.Unmarshal(buf.Bytes(), &m) // m["msg"], m["level"], m["time"], m["k"], при span в ctx — trace_id/span_id
```

OTel-sink проверяется через `sdklog.NewLoggerProvider` с `SimpleProcessor` и
своим in-memory `sdklog.Exporter` (см. `otel_test.go`). `Nop()` — заглушка,
не паникует и ничего не пишет.

## Инварианты и подводные камни

- **В production только `logkit.Logger`.** `fmt.Print*` и `slog.Default()`
  обходят trace-correlation, redact, метрику и единый формат.
- **Redact выключен по умолчанию** и применяется до fan-out: маскируются оба
  sink'а. Маска работает по строкам — `msg`, строковые атрибуты (в т.ч. из
  `With()` и групп), текст `error`, JSON-представление структур. Числа не
  трогаются, поэтому unix-ms и наносекундные длительности остаются числами.
  Дефолтные паттерны — эвристики; подбирайте свои под данные сервиса.
- **`LOG_STDOUT=false` без OTel-sink'а = записи никуда не идут.**
- **Ошибка конфигурации — при конструировании**, не на первой записи:
  невалидный `LOG_LEVEL`/regex валит старт.
- **`RegisterAttr` глобален и аддитивен.** Регистрация складывается; зовите
  при старте.
- **`log_messages_total` считает вызовы**, включая отфильтрованные по уровню.
