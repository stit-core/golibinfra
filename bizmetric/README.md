# bizmetric

Конвенциональный helper для бизнес-метрик. Тонкая обёртка над OTel-инструментами
(`metric.Meter`), которая форсит единую конвенцию имён `biz_<domain>_<name>_<suffix>`
и валидирует `domain`/`name` — невалидное имя ловится panic'ом на старте, а не
молча уезжает в прод кривой метрикой.

## Назначение

`bizmetric` держит единую конвенцию имён бизнес-метрик. Все они носят префикс
`biz_`, чтобы их можно было единообразно выбрать по `{__name__=~"biz_.*"}` без
ручного списка метрик. Helper делает эту конвенцию неизбежной: имя нельзя собрать
руками в обход — иначе panic.

Не плодите biz-метрики, регистрируя OTel-инструмент напрямую с произвольным
именем: конвенция перестанет держаться. Если нужен ручной инструмент (например
observable gauge), имя всё равно собирайте через [`MustName`](#mustname) — он
валидирует `domain`/`name` тем же правилом.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/bizmetric"
```

Версионирование и установка модуля — в [корневом README](../README.md)
(§ Версионирование, § Стабильность API). Здесь версию не дублируем.

## Быстрый старт

`meter` — это `metric.Meter` от OTel SDK (потребитель поднимает свой
`MeterProvider`; без него инструменты — no-op). `domain` и `name` — два уровня
имени; helper сам добавит префикс `biz_` и суффикс по типу инструмента.

```go
import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/bizmetric"
)

// Counter → biz_auth_login_attempts_total
logins := bizmetric.Counter(meter, "auth", "login_attempts")
logins.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "ok")))

// Histogram (seconds) → biz_onboarding_duration_seconds
onboarding := bizmetric.Histogram(meter, "onboarding", "duration")
onboarding.Record(ctx, 1.5)

// Gauge → biz_checkout_active_carts (текущее значение)
carts := bizmetric.Gauge(meter, "checkout", "active_carts")
carts.Record(ctx, 42)

// UpDownCounter → biz_checkout_carts_open (дельты)
open := bizmetric.UpDownCounter(meter, "checkout", "carts_open")
open.Add(ctx, 1)
```

## Конфигурация

Конфига нет. У kit'а нет публичной конфиг-структуры и нет fx-`Module` —
это набор stateless-функций. Всё, что нужно, — передать `metric.Meter`.

## Observability

### Как формируется имя

Имя метрики собирается строго как `biz_<domain>_<name>_<suffix>`, где суффикс
определяется типом инструмента:

| Helper | OTel-инструмент | Суффикс | Unit | Пример имени |
|---|---|---|---|---|
| `Counter` | `Int64Counter` | `_total` | `{event}` | `biz_auth_login_attempts_total` |
| `Histogram` | `Float64Histogram` | `_seconds` | `s` | `biz_onboarding_duration_seconds` |
| `Gauge` | `Int64Gauge` | *(нет)* | `{item}` | `biz_checkout_active_carts` |
| `UpDownCounter` | `Int64UpDownCounter` | *(нет)* | `{item}` | `biz_checkout_carts_open` |

`domain` и `name` должны соответствовать `^[a-z][a-z0-9_]*$` — snake_case ASCII:
строчные буквы, цифры и `_`, первый символ — буква. Префикс `biz_` подставляет
helper, суффикс — тип инструмента: `domain`/`name` с префиксом `biz_` или с
зарезервированным суффиксом (`_total`, `_seconds`, `_count`, `_sum`, `_bucket`,
`_created`) отклоняются panic'ом. Первые два подставляет helper, остальные —
Prometheus-трансляция гистограмм и счётчиков; своё имя с таким суффиксом
столкнулось бы с чужой серией.

### Unit

Unit ставится по умолчанию и переопределяется своим `metric.WithUnit(...)` в
`opts` — пользовательские опции идут после дефолтных. Значения в фигурных скобках
(`{event}`, `{item}`) — аннотации по OTel-спецификации: OTLP-native бэкенды
показывают их как есть, Prometheus-трансляция отбрасывает и имя метрики не
меняет. Константы — `bizmetric.UnitEvent`, `UnitItem`, `UnitSeconds`.

### Бакеты гистограмм

`Histogram` получает общие для репо границы (секунды):
`0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30` — те же, что у
`httpkit` и остальных `_seconds`-метрик, чтобы квантили считались по одной сетке.
Своя сетка — `metric.WithExplicitBucketBoundaries(...)` в `opts`.

### Labels

Размерности метрики — через OTel-атрибуты в момент записи, не через имя:

```go
logins.Add(ctx, 1, metric.WithAttributes(
	attribute.String("result", "ok"),
	attribute.String("method", "password"),
))
```

### Resource attrs

Идентичность сервиса (`service.name`, `service.version`,
`deployment.environment.name`) в labels руками **не** дублируется: её несёт
OTel-resource, который настраивает потребитель в своей проводке OTel SDK.

## API

Все функции принимают `meter metric.Meter` первым аргументом и возвращают
готовый OTel-инструмент. Variadic-`opts` пробрасываются в соответствующий
конструктор meter'а как есть.

```go
func Counter(meter metric.Meter, domain, name string, opts ...metric.Int64CounterOption) metric.Int64Counter
func Histogram(meter metric.Meter, domain, name string, opts ...metric.Float64HistogramOption) metric.Float64Histogram
func Gauge(meter metric.Meter, domain, name string, opts ...metric.Int64GaugeOption) metric.Int64Gauge
func UpDownCounter(meter metric.Meter, domain, name string, opts ...metric.Int64UpDownCounterOption) metric.Int64UpDownCounter
func MustName(domain, name string) string

const UnitEvent = "{event}"
const UnitItem = "{item}"
const UnitSeconds = "s"
```

### Counter

Создаёт `Int64Counter` с именем `biz_<domain>_<name>_total`. Для монотонных
счётчиков событий.

### Histogram

Создаёт `Float64Histogram` с именем `biz_<domain>_<name>_seconds`. Суффикс
`_seconds` фиксирован — kit рассчитан на длительности в секундах.

### Gauge

Создаёт `Int64Gauge` с именем `biz_<domain>_<name>` (без суффикса): `Record`
пишет текущее значение величины («сейчас 42 корзины»). Для observable-источников
(значение читается коллбэком) используйте `meter.Int64ObservableGauge` напрямую,
а имя соберите через `MustName`.

### UpDownCounter

Создаёт `Int64UpDownCounter` с именем `biz_<domain>_<name>` (без суффикса):
`Add` пишет дельту (`+1` на открытие корзины, `-1` на закрытие). Берите его, когда
у кода есть события изменения, но нет доступа к текущему значению; когда значение
известно целиком — `Gauge`.

### MustName

Собирает и валидирует имя `biz_<domain>_<name>` без создания инструмента. Нужен,
когда инструмент регистрируется вручную (ObservableGauge/Counter), но имя должно
остаться в конвенции. Panic-ует на невалидные `domain`/`name`.

## Тестирование

Хелперы получают `Meter` аргументом, поэтому запись бизнес-метрики проверяет
провайдер теста `testkit/metrictest`: сервис в тесте получает его `Meter`, снимок
видит только записи теста — тип, unit, labels и значение серии, без дельт и
параллельно с другими тестами.

```go
p := metrictest.NewProvider()
svc := NewService(NewMetrics(p.Meter("test"))) // в приложении Meter приходит из графа
svc.Login(ctx, creds)
snap := p.Collect(t)
s := snap.Get(t, "biz_auth_login_attempts_total", "result", "ok")
// s.Kind == metrictest.Counter, s.Unit == bizmetric.UnitEvent, s.Value == 1;
// snap.Value("biz_auth_login_attempts_total") == 1 — исход ровно один, лишней серии нет
```

## Инварианты и подводные камни

- **Panic при нарушении конвенции.** `domain` или `name` вне `^[a-z][a-z0-9_]*$`
  (заглавные буквы, дефис, пустая строка, первый символ — цифра), с префиксом
  `biz_` или зарезервированным суффиксом (`_total`, `_seconds`, `_count`, `_sum`,
  `_bucket`, `_created`) — `panic`. Это
  fail-fast: метрика с кривым именем не должна доехать до прода. Так как имена
  обычно регистрируются при старте сервиса, нарушение всплывёт сразу.

- **Panic при ошибке создания инструмента.** Если meter вернул ошибку на
  `Int64Counter`/`Float64Histogram`/`Int64Gauge`/`Int64UpDownCounter`, helper тоже
  паникует с именем метрики в сообщении — невалидная регистрация не маскируется.

- **Не собирать имя в обход helper'а.** Регистрация OTel-инструмента напрямую с
  произвольной строкой ломает конвенцию `biz_*`. Любую biz-метрику заводите через
  `Counter`/`Histogram`/`Gauge`/`UpDownCounter`, а ручные случаи — через `MustName`.

- **Префикс/суффикс не дублировать.** `biz_` в начале и зарезервированные
  суффиксы в конце `domain`/`name` отклоняются. `_` внутри `name` допустим
  (`login_attempts`, `total_orders`, `count_of_items`) — суффиксом считается
  только окончание строки.

- **`domain` vs `name`.** `domain` — бизнес-область (`auth`, `checkout`,
  `onboarding`); `name` — конкретное событие/величина (`login_attempts`,
  `orders_completed`, `active_carts`). Оба — обязательные, оба валидируются.

- **Конфликт типа на одном имени.** Имя метрики в OTel уникально в пределах
  meter'а: зарегистрировать одно и то же `biz_<domain>_<name>` и как Counter, и
  как другой инструмент нельзя — meter вернёт ошибку, helper её превратит в panic.
  `Gauge` и `UpDownCounter` дают имя без суффикса, поэтому на одной паре
  `domain`/`name` они конфликтуют между собой.
