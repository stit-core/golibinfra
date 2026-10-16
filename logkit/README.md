# logkit

Структурный `Logger` поверх `log/slog`: JSON или text в локальный sink
(stdout либо произвольный `io.Writer`), опциональный второй sink в OTel
`LoggerProvider` (OTLP), PII-redact на уровне атрибутов, `trace_id`/`span_id`
из `ctx`, `RegisterAttr` и метрика `log_messages_total{level}`.

## Назначение

`logkit` — единая точка логирования сервисов stit. Одна цепочка handler'ов,
один формат записи, две точки выхода:

- **Локальный sink** — то, что собирает `docker logs` / файловый сборщик:
  JSON (`LOG_FORMAT=json`) или human-readable text (`LOG_FORMAT=text`, для
  локальной разработки). При `LOG_SILENT=true` записи отбрасываются
  (`io.Discard`); произвольный writer — опция `WithWriter`.
- **OTel Logs (OTLP)** — второй sink, включается опцией `WithOTel` или
  автоматически через fx, если в графе есть `log.LoggerProvider`
  (его отдаёт `telemetry.Module`). Уровень, `msg`, атрибуты и группы маппятся в
  `log.Record`; `trace_id`/`span_id` SDK берёт из `ctx` и кладёт в поля записи.
- **trace-correlation** — при валидном span context в `ctx` запись локального
  sink'а получает `trace_id`/`span_id`.
- **`RegisterAttr`** — глобально регистрируемые поля из `ctx` (так `httpkit`
  пробрасывает `request_id`); попадают в оба sink'а. Регистрация ключевая:
  повторный вызов с тем же ключом заменяет экстрактор, а не добавляет второй.
- **PII-redact** — включён всегда, пока его не выключат явно
  (`LOG_REDACT_DISABLED=true`); `WithRedact` + `RedactConfig` задают свои маски
  и замену: regex-маски применяются к `msg` и строковым
  значениям атрибутов, включая `With()`-attrs, группы, `error` и
  JSON-представление `Any`-значений, а значение под ключом-секретом (`token`,
  `password`, `secret`, `authorization`, `cookie`, …) заменяется целиком; числа
  (кроме ключей-кодов `otp`, `pin`), `bool`, `time`, `duration` не трогаются.
  Работает до fan-out — маскируются оба sink'а.
- **Fail-fast** — невалидный `LOG_LEVEL`/`LOG_FORMAT`, regex в
  `LOG_REDACT_PATTERNS` или `LOG_REDACT_REPLACE_WITH` дают ошибку конструктора
  (в fx — отказ сборки графа).

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
зависимости: без `RedactConfig` маскирует дефолтный набор, без `LoggerProvider`
OTel-sink не добавляется. `NewRedactConfig` добавляют в граф, чтобы маскирование
настраивали `LOG_REDACT_*`.

```go
fx.New(
	fx.Provide(telemetry.NewConfig),     // нужен telemetry.Module (OTEL_*)
	fx.Provide(logkit.NewConfig),        // Config из ENV (LOG_*)
	fx.Provide(logkit.NewRedactConfig),  // RedactConfig из ENV (LOG_REDACT_*)
	telemetry.Module,                    // опционально: даёт log.LoggerProvider → OTLP-sink
	logkit.Module,                       // → logkit.Logger, *slog.Logger
	logkit.FxLogger,                     // события fx — в локальный лог
	fx.Invoke(func(lc fx.Lifecycle, log logkit.Logger) {
		lc.Append(fx.Hook{
			OnStart: func(ctx context.Context) error {
				log.Info(ctx, "service started")
				return nil
			},
		})
	}),
)
```

`telemetry.Module` требует в графе `telemetry.Config` и `logkit.Config` —
без первого сборка графа падает. Граф без `telemetry.Module` собирается и
работает: OTLP-sink не подключается, локальный sink пишет как обычно.

`FxLogger` (`fx.WithLogger`) пишет события fx логгером из `Config` и `RedactConfig`
графа — формат, маски и локальный sink те же, что у `Module`, OTel-sink'а нет: логгер
событий не зависит от графа `logkit.Module`, поэтому отказ того, что этот граф тянет
(ошибка конфигурации `telemetry` — `log.LoggerProvider`), — тоже запись `ERROR`, а не
текст fx в stderr. Ошибки сборки и старта (конструктор, `Invoke`, `OnStart`/`OnStop`,
откат) — `ERROR` с полем `error`, рутина (`provided`, `invoking`, хуки, `started`) —
`DEBUG`. При `LOG_SILENT=true` записи `ERROR` идут в stderr: stdout заглушён, второго
sink'а у логгера событий нет, а причина отказа старта теряться не должна. Опция ставится
на верхний уровень `fx.New`; вместе с `fx.RecoverFromPanics()` паника конструктора или
`Invoke` становится такой же ошибкой старта.

### Без fx

Команда вне fx (миграции, разовые задачи) получает логгер одной строкой:

```go
log, err := logkit.NewFromEnv() // LOG_* и LOG_REDACT_* — как граф с Module; негодное значение — ErrConfig
```

`NewFromEnv` читает `Config` (`NewConfig`) и `RedactConfig` (`NewRedactConfig`) тем же ридером ENV и с теми же
дефолтами и проверками, что граф `fx.Provide(logkit.NewConfig, logkit.NewRedactConfig)` с `Module`. OTel-sink'а
у этого логгера нет, поэтому при `LOG_SILENT=true` записи `ERROR` идут в stderr (`WithStderrFallback`): причина
отказа команды не теряется. Свой конфиг или опции — `New`:

```go
cfg, err := logkit.NewConfig() // или logkit.Config{Level: "debug"}
log, err := logkit.New(cfg)    // Logger в stdout, маскирование — дефолтным набором
sl, err := logkit.NewSlog(cfg) // *slog.Logger той же цепочки
```

Опции `New`/`NewSlog` комбинируются в одном вызове:

```go
log, err := logkit.New(cfg,
	logkit.WithWriter(w),            // writer вместо stdout
	logkit.WithRedact(redactCfg),    // свои PII-маски и замена; Disabled выключает
	logkit.WithOTel(loggerProvider), // второй sink; WithoutOTel() отменяет заданный ранее
	logkit.WithService(svcCfg),      // service/env/version в каждой записи
)
```

```go
log := logkit.FromSlog(sl) // одна цепочка для обеих форм
```

`FromSlog` нужен там, где `*slog.Logger` уже построен (или пришёл из графа):
обе формы делят цепочку handler'ов и один счётчик записей. `FromSlog(nil)`
даёт `Nop()`.

### Логирование с ctx и trace_id

```go
func (h *Handler) Do(ctx context.Context, log logkit.Logger) {
	log.Info(ctx, "processing order", "order_id", id, "amount", 1500)
	// → {"time":…,"level":"INFO","msg":"processing order","order_id":…,"amount":1500,"trace_id":…,"span_id":…}
}
```

Сигнатуры — `Debug/Info/Warn/Error(ctx, msg string, kv ...any)`; `kv` — пары
ключ-значение в стиле slog. `With(kv ...any)` возвращает `Logger` с
прикреплёнными полями. `Enabled(ctx, level)` позволяет пропустить дорогую
подготовку атрибутов:

```go
if log.Enabled(ctx, slog.LevelDebug) {
	log.Debug(ctx, "payload", "body", expensiveDump(req))
}
```

### RegisterAttr — проброс полей из ctx

`RegisterAttr` регистрирует под ключом функцию, которую kit зовёт на **каждой**
записи; при `ok == true` поле добавляется в record (в оба sink'а, всегда на
верхнем уровне записи). Значение — любого типа (потребителю `log/slog` не нужен),
но, в отличие от пар ключ-значение `Logger`, идёт мимо redact: PII маскирует сам
экстрактор (§ Инварианты и подводные камни).

```go
logkit.RegisterAttr("request_id", func(ctx context.Context) (any, bool) {
	rid, ok := ctx.Value(requestIDKey{}).(string)
	return rid, ok
})
```

Пустой ключ или `nil`-функция — паника на старте. Повторная регистрация того же
ключа заменяет предыдущую, поэтому граф, собираемый дважды (`fx.New` в тестах и
в `main`), не даёт дублей поля.

### Redact

```go
log, err := logkit.New(cfg) // маскирует без опций; logkit.WithRedact — свои маски
log.With("owner", "owner@example.com").Info(ctx, "login john@example.com", "phone", "+79161234567", "elapsed", 1500*time.Millisecond)
// → "msg":"login [REDACTED]", "owner":"[REDACTED]", "phone":"[REDACTED]", "elapsed":1500000000
```

## Конфигурация

Конфиг-структуры читаются ридером ENV китов. Имена полей и envconfig-теги —
**part of public API** (корневой README § Стабильность API).

### `Config`

Zero-value валиден: `info`, JSON, stdout, без `source`.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `LOG_LEVEL` | `Level` | `string` | `info` | Минимальный уровень (`debug`/`info`/`warn`/`error`, регистр не важен; пустая строка = `info`). Невалидное значение → `ErrConfig`. |
| `LOG_FORMAT` | `Format` | `string` | `json` | `json` — прод и сборщики логов; `text` — локальная разработка. Другое значение → `ErrConfig`. |
| `LOG_SOURCE` | `Source` | `bool` | `false` | Добавляет `source` (файл/строка/функция вызывающего) в каждую запись. |
| `LOG_SILENT` | `Silent` | `bool` | `false` | `true` — локальный sink отбрасывает записи (OTel-sink, если подключён, работает). `WithWriter` приоритетнее: явный writer пишет и при `LOG_SILENT=true`. Логгеры без OTel-sink'а — событий fx (`FxLogger`), команды (`NewFromEnv`) и ошибок экспорта `telemetry` — пишут записи `ERROR` в stderr (`WithStderrFallback`). |

Поле `time` — RFC3339 с offset локальной зоны процесса: в JSON — до наносекунд
(`2026-05-14T10:30:00.123456789+03:00`), в `text` — до миллисекунд; для UTC-времени
запускайте процесс с `TZ=UTC`.

### `RedactConfig`

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `LOG_REDACT_DISABLED` | `Disabled` | `bool` | `false` | `true` выключает redact-слой. Нулевой `RedactConfig{}`, граф без `RedactConfig` и логгер без `WithRedact` маскируют дефолтным набором. Паттерны и замену выключенного проверяют `Validate` и `NewRedactConfig`; конструктор логгера с `WithRedact` и `Disabled` их не компилирует. |
| `LOG_REDACT_PATTERNS` | `Patterns` | `Patterns` (`[]string`) | — (дефолтные маски) | Regex-фильтры; в ENV разделитель `;` (запятая встречается в квантификаторах `{10,15}`), литеральная `;` внутри паттерна пишется как `\;`. Невалидный regex → `ErrConfig`. Паттерн, совпадающий с пустой строкой (`a*`, `x?`, `""` в литерале `Patterns`), тоже → `ErrConfig`: он вставляет замену между каждой парой символов записи. Пустой элемент списка в ENV пропускается: `a;;b` — два паттерна, значение из одних `;` и пробелов — дефолтные маски. |
| `LOG_REDACT_REPLACE_WITH` | `ReplaceWith` | `string` | `[REDACTED]` | Строка-замена, подставляется литерально (`$1` не раскрывается). `"`, `\` и control-символы отвергаются (`ErrConfig`): они ломают JSON-представление структурных значений. |

Дефолтный набор (применяется, когда `Patterns` пуст) — эвристики компактных
форм PII и очевидных секретов:

| Что | Паттерн |
|---|---|
| email | `[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}` |
| телефон | `\+\d{10,15}\b\|\b[78]\d{10}\b` |
| карта | `\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b` |
| `Authorization: Bearer` | `(?i)\bbearer\s+[A-Za-z0-9\-._~+/]+=*` |
| `Authorization: Basic` | `(?i)\bbasic\s+[A-Za-z0-9+/]{8,}={0,2}` |
| JWT | `\beyJ[A-Za-z0-9_-]{6,}\.eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]+` |
| Anthropic API key | `\bsk-ant-[A-Za-z0-9_-]{16,}` |
| generic API key | `\bsk-[A-Za-z0-9]{20,}\b` |
| hex-цепочка ≥ 32 (токены, MD5/SHA-1/SHA-256) | `[0-9A-Fa-f]{32,}` |
| токен base64url ≥ 20 с буквами обоих регистров и цифрой (ключи API, токены сброса и сессий) | `[A-Za-z0-9_-]{20,}` + проверка состава |
| пароль userinfo URL (`postgres://app:…@db`) до последнего `@` слова — и с `@`, `/`, `?`, `#` в пароле | `[A-Za-z][A-Za-z0-9+.-]*://[^\s/?#@:]*:(\S+)@` |
| значение пары `ключ = значение` с ключом-секретом по словарю ниже (query, keyword-DSN `password=…` и `password = '…'`, `otpauth://…?secret=…`): в кавычках целиком, иначе до пробела, `&` или кавычки (с `#`, `=`, `;` внутри); число — только у ключа-кода | сканер `maskPairs`: ключ — `[A-Za-z0-9_.-]+` перед `=`, значение не начинается с `=` (`token == nil` — не пара); значение пары с обычным ключом не съедается — вложенные пары (`redirect=https://x/?token=…`) видны |

Десятизначные идентификаторы без `+`/ведущей `7`/`8`, UUID с дефисами, hex
короче 32 символов, base64url короче 20 символов или без букв обоих регистров и
цифры (ULID, имена подов, слова) и пары с обычным ключом (`page=2`) не маскируются;
полный SHA коммита, UUID без дефисов и переданный атрибутом `trace_id` — hex от 32
символов, их маска прячет (собственные `trace_id`/`span_id` logkit добавляет после
redact). Путь URL отдельно не маскируется: токен в пути прячет только маска токена
base64url. Строку заголовка в тексте — в `msg` или строковом значении (`Cookie: session=…`,
`Authorization: Token …`) — прячут только маски значений (`Bearer`, `Basic`, JWT, токен
base64url): слово перед `:` ключом не считается. Заголовок пишут атрибутом с ключом-секретом
(`"cookie", v`) или `http.Header` целиком — их ключи проверяются (ниже). Свой набор задаётся
целиком — дефолтные при этом не применяются.

Дефолтный набор проходит литеральный pre-filter: строка без `@`, `+`, `bearer`,
`basic`, `eyJ`, `sk-`, без цепочки от 11 цифр, без 32 hex-символов и токена base64url
подряд и без `=` после ключа со словом ключа-секрета (пробелы у `=` — тоже) до regexp'а не доходит. Это и держит цену: прогонка объединённого regexp'а стоит
O(длина × размер программы), а запись без маркеров секрета обходится сканом по
байтам — около ×2 к той же записи без redact'а; запись со структурой,
`http.Header` или срезом `map` — не дороже ×3 (потолок держит
`TestRedact_CostOfRecordWithoutSecrets`).

Свой набор pre-filter не проходит: его regexp прогоняется по каждой строке и по
JSON-байтам структурных значений, поэтому даже одна узкая маска на записи со
структурой стоит дороже отфильтрованного дефолтного набора
(`BenchmarkLoggerInfoRedactStruct` против `…NarrowSet`).

Кроме масок, redact смотрит на ключ атрибута — и при дефолтном, и при своём
наборе. Ключ режется на слова по разделителям, camelCase и цифрам (`X-Api-Key`,
`accessToken`, `oauth2Token`); если слово совпадает со словом-секретом или
оканчивается на него (и во множественном числе: `secrets`, `passwords`) — `token`,
`password`, `passwd`, `pwd`, `passphrase`, `secret`, `signature`, `authorization`,
`cookie`, `credential`, `jwt`, `apikey`/`api_key`/`api_keys`/`XAPIKey`,
`privatekey`/`private_key`, `code_verifier` (`accesstoken`, `newpassword` тоже) — или
целым словом `pass`, `pw`, `cred`/`creds`, `sid`, `refresh`, `tokens`, значение
заменяется целиком: строка, группа и структура (`Any`). Сравнение по словам, поэтому
`tokenizer`, `bypass`, `sacred`, `session_id` и `cache_key` — не секреты. Числа, `bool` и `nil`
под таким ключом остаются (`token_count`, `input_tokens` — счётчики). Код — ключ со
словом `pin`, `pincode`, `passcode`, словом на `otp` (`otp`, `totp`) или словом `code`
после `verification`, `auth`, `oauth`, `sms`, `email`, `reset`, `login`, `mfa` и
подобных (`verification_code`, `smsCode`): под ним прячется значение любого вида, и
число тоже, — если ключ не кончается словом-счётчиком (`otp_attempts`, `pin_length`,
`otp_ttl_seconds` — счётчики). Голый `code` (код ошибки httpkit), `status_code` и
`error_code` — не коды.

Те же правила действуют для ключей внутри структурного значения (`Any`): redact
проверяет ключи его JSON-представления на любой глубине — `map` (`http.Header`,
`url.Values`), поля структур под именами `encoding/json` (теги, встроенные структуры),
значения за указателями, вывод своего `MarshalJSON`, JSON из `json.RawMessage` и из
`[]byte`, JSON внутри строки и `[]byte` — атрибута, `msg` и строкового поля структуры (тело
запроса). Маскируются только значения под ключами-секретами; порядок полей и
остальной JSON — как без маскирования. Оборванный или битый JSON (тело, обрезанное по
лимиту, цитата в тексте ошибки) маскируется до места обрыва: значение под ключом-секретом,
которое не закрыто, прячется до конца строки. Цикл (`m["self"] = m`) запись получает как
`!ERROR … cycle`, как без маскирования. Значение, которое не кодируется в JSON
(`*http.Request`, структура с `func` или `chan`), во всех sink'ах — строка
`!ERROR:<ошибка кодирования>`: text и OTel не печатают его через `%+v` с заголовками и
полями.

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

- **`log_messages_total`** (counter, label `level` ∈ `debug`/`info`/`warn`/`error`)
  — через `otel.Meter("git.stit.tech/stit-core/golibinfra/logkit")`. Считает
  **эмитированные записи**: инкремент стоит в handler'е, после проверки уровня,
  поэтому отфильтрованные вызовы (`Debug` при `LOG_LEVEL=info`) не считаются и
  не аллоцируют. Записи через `*slog.Logger` из той же цепочки учитываются
  наравне с `logkit.Logger`. Без `MeterProvider` — no-op.
- **OTel-sink** — `log.Record` с `Timestamp`, `Severity` (Debug 5 / Info 9 /
  Warn 13 / Error 17), `SeverityText`, `Body = msg`, атрибутами (группы —
  вложенные map-значения; группа с пустым ключом инлайнится на текущий уровень;
  `duration` — int64 ns; `time` — RFC3339Nano; `error` — строка; `[]byte` —
  bytes; прочие `Any` — JSON-строка) и trace/span id из `ctx`.
  Instrumentation scope — `git.stit.tech/stit-core/golibinfra/logkit`,
  `otel_scope_version` — версия golibinfra в сборке (как у метрик и спанов
  остальных китов). Уровень фильтрации тот же `LOG_LEVEL`.
- **Порядок ключей внутри группы** в OTel-sink'е — алфавитный: так их
  укладывает `attribute.MapValue`. Локальный JSON-sink сохраняет порядок вызова;
  атрибуты верхнего уровня порядок сохраняют в обоих sink'ах.
- Спаны kit не эмитит.

## API

```go
type Logger interface {
	Debug(ctx context.Context, msg string, kv ...any)
	Info(ctx context.Context, msg string, kv ...any)
	Warn(ctx context.Context, msg string, kv ...any)
	Error(ctx context.Context, msg string, kv ...any)
	With(kv ...any) Logger
	Enabled(ctx context.Context, level slog.Level) bool
}

func New(cfg Config, opts ...Option) (Logger, error)
func NewFromEnv() (Logger, error)               // команда вне fx: LOG_* и LOG_REDACT_*, как граф с Module; при LOG_SILENT ERROR — в stderr
func NewSlog(cfg Config, opts ...Option) (*slog.Logger, error)
func FromSlog(l *slog.Logger) Logger            // nil → Nop()
func Nop() Logger

func WithWriter(w io.Writer) Option             // приоритетнее Config.Silent
func WithStderrFallback() Option                // при Config.Silent записи ERROR — в stderr: логгеру без OTel-sink'а
func WithRedact(cfg RedactConfig) Option       // без опции — дефолтный набор
func WithOTel(lp log.LoggerProvider) Option     // nil → sink не добавляется
func WithoutOTel() Option
func WithService(cfg ServiceConfig) Option

func NewRedactHandler(inner slog.Handler, cfg RedactConfig) (slog.Handler, error) // обёртка над любым slog.Handler; nil inner → ошибка
func NewOTelHandler(lp log.LoggerProvider, level slog.Leveler) slog.Handler       // мост slog → OTel Logs; nil-аргументы → no-op sink и info

type Attr func(ctx context.Context) (any, bool) // значение любого типа, как у пар Logger
func RegisterAttr(key string, fn Attr)          // дедупликация по ключу

func NewConfig() (Config, error)                // ENV + Validate
func NewRedactConfig() (RedactConfig, error)    // ENV + Validate
func NewServiceConfig() (ServiceConfig, error)  // ENV
func (c Config) Validate() error
func (c RedactConfig) Validate() error

var ErrConfig error                             // невалидный уровень, формат, regex или строка-замена
var Module fx.Option                            // provides *slog.Logger, Logger; optional: RedactConfig, log.LoggerProvider
var FxLogger fx.Option                          // fx.WithLogger: события fx логгером из Config и RedactConfig графа, без OTel; ошибки — ERROR (при LOG_SILENT — в stderr), рутина — DEBUG
```

Цепочка handler'ов: redact → метрика → `RegisterAttr`-атрибуты → fan-out
(локальный sink с `trace_id`/`span_id`; OTel). `NewSlog` отдаёт `*slog.Logger`
с той же цепочкой — для сторонних библиотек, принимающих `*slog.Logger`.

## Тестирование

```go
var buf bytes.Buffer
log, _ := logkit.New(logkit.Config{Level: "debug"}, logkit.WithWriter(&buf))
log.Info(ctx, "hello", "k", "v")

var m map[string]any
_ = json.Unmarshal(buf.Bytes(), &m) // m["msg"], m["level"], m["time"], m["k"], при span в ctx — trace_id/span_id
```

OTel-sink проверяется через `sdklog.NewLoggerProvider` с `SimpleProcessor` и
своим in-memory `sdklog.Exporter` (см. `otel_test.go`). `Nop()` — заглушка,
не паникует, ничего не пишет и на `Enabled` отвечает `false`.

Сниппеты § Быстрый старт закреплены тестами `readme_test.go`: fx-граф — через
`fx.ValidateApp`, вариант без fx — компиляцией и прогоном.

## Инварианты и подводные камни

- **В production только `logkit.Logger`.** `fmt.Print*` и `slog.Default()`
  обходят trace-correlation, redact, метрику и единый формат.
- **ctx-атрибуты всегда на верхнем уровне записи.** `trace_id`/`span_id`/
  `RegisterAttr`-поля не уезжают внутрь `WithGroup`-группы: под открытыми
  группами kit добавляет их к корню и реплеит цепочку `With*`. Реплей стоит
  аллокаций только при открытых группах.
- **`RegisterAttr`-значения и `trace_id`/`span_id` идут мимо redact:** они
  добавляются после redact-слоя. Экстрактор, отдающий PII (например `user_id`
  = email-subject), маскировать нужно на своей стороне.
- **Redact включён всегда**, пока его не выключит `RedactConfig.Disabled`, и применяется до
  fan-out: маскируются оба sink'а. Маска работает по строкам — `msg`,
  строковые атрибуты (в т.ч. из `With()` и групп), текст `error`,
  JSON-представление структур (`Any`), содержимое `[]byte`; значение под
  ключом-секретом заменяется целиком. Числа не трогаются (кроме ключей-кодов —
  `otp`, `pin`, `verification_code`), поэтому unix-ms и наносекундные длительности
  остаются числами.
  Паттерн, «съедающий» кавычку, сделал бы JSON невалидным — такое значение
  уходит в лог замаскированной строкой, а не теряется. Дефолтные паттерны —
  эвристики, и только они проходят pre-filter; свой набор — это прогонка
  regexp по каждой строке и по JSON-байтам структурных значений, что при
  высоком log-rate заметно.
- **`LOG_SILENT=true` без OTel-sink'а = записи никуда не идут.** Исключение — логгеры,
  у которых OTel-sink'а нет по построению: `FxLogger`, логгер команды `NewFromEnv` и логгер
  ошибок экспорта `telemetry.Module` пишут записи `ERROR` в stderr (`WithStderrFallback`).
- **`telemetry.Module` требует `telemetry.Config` в графе.** Он же поднимает
  OTLP-sink, который `logkit.Module` подхватывает опциональной зависимостью:
  без `telemetry.Module` логи идут только в локальный sink.
- **Ошибка конфигурации — при конструировании**, не на первой записи:
  невалидный `LOG_LEVEL`/`LOG_FORMAT`/regex/`ReplaceWith` валит старт. Этой
  ошибки `FxLogger` не видит: логгер событий fx строится из того же конфига, и fx
  пишет причину текстом в stderr.
- **`FxLogger` — только на верхнем уровне `fx.New`.** Внутри `fx.Module` опция
  ловит лишь события этого модуля, остальные идут в stderr fx.
- **`RegisterAttr` глобален.** Регистрация по ключу идемпотентна (последняя
  побеждает); зовите при старте.
- **`log_messages_total` считает эмитированные записи** обеих форм логгера;
  отфильтрованные по уровню вызовы в метрику не попадают.
