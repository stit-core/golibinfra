# bizmetric

Конвенциональный helper для бизнес-метрик. Тонкая обёртка над OTel-инструментами
(`metric.Meter`), которая форсит единую конвенцию имён `biz_<domain>_<name>_<suffix>`
и валидирует `domain`/`name` — невалидное имя ловится panic'ом на старте, а не
молча уезжает в прод кривой метрикой.

## Назначение

`bizmetric` — корп observability-инвариант. Все бизнес-метрики сервисов stit носят
префикс `biz_`, чтобы дашборд `Apps · Business` мог автодискаверить их по
`{__name__=~"biz_.*"}` без ручного списка метрик. Helper делает эту конвенцию
неизбежной: имя нельзя собрать руками в обход — иначе panic.

`biz_*` — единственная разрешённая конвенция имён business-метрик в корп-стеке.
Не плодите biz-метрики, регистрируя OTel-инструмент напрямую с произвольным именем:
тогда дашборд их не подхватит, а инвариант перестанет держаться. Если нужен ручной
инструмент (например observable gauge), имя всё равно собирайте через
[`MustName`](#mustname) — он валидирует `domain`/`name` тем же правилом.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/bizmetric"
```

Версионирование и установка модуля — в [корневом README](../README.md)
(§ Версионирование, § Стабильность API). Здесь версию не дублируем.

## Быстрый старт

`meter` — это `metric.Meter`, полученный от OTel SDK (его поднимает `telemetry`).
`domain` и `name` — два уровня имени; helper сам добавит префикс `biz_` и суффикс
по типу инструмента.

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

// Gauge → biz_checkout_active_carts
carts := bizmetric.Gauge(meter, "checkout", "active_carts")
carts.Add(ctx, 1)
```

## Конфигурация

Конфига нет. У kit'а нет публичной конфиг-структуры и нет fx-`Module` —
это набор stateless-функций. Всё, что нужно, — передать `metric.Meter`.

## Observability

### Как формируется имя

Имя метрики собирается строго как `biz_<domain>_<name>_<suffix>`, где суффикс
определяется типом инструмента:

| Helper | OTel-инструмент | Суффикс | Пример имени |
|---|---|---|---|
| `Counter` | `Int64Counter` | `_total` | `biz_auth_login_attempts_total` |
| `Histogram` | `Float64Histogram` | `_seconds` | `biz_onboarding_duration_seconds` |
| `Gauge` | `Int64UpDownCounter` | *(нет)* | `biz_checkout_active_carts` |

`domain` и `name` должны соответствовать `^[a-z][a-z0-9_]*$` — snake_case ASCII:
строчные буквы, цифры и `_`, первый символ — буква. Префикс `biz_` и суффикс
(`_total`/`_seconds`) подставляет helper, в `domain`/`name` их класть не нужно.

### Labels

Размерности метрики — через OTel-атрибуты в момент записи, не через имя:

```go
logins.Add(ctx, 1, metric.WithAttributes(
	attribute.String("result", "ok"),
	attribute.String("method", "password"),
))
```

### Resource attrs

`service.name`, `service.version`, `deployment.environment.name`, `tenant`,
`service.namespace` в labels руками **не** дублируются: их добавляет OTel SDK
(из `telemetry.Config`) и корп otel-collector
(`resource_to_telemetry_conversion: true`). См. корневой README
§ Minimum viable observability.

Дашборд `Apps · Business` рендерит панели для всех `biz_*_total` автоматически.

## API

Все функции принимают `meter metric.Meter` первым аргументом и возвращают
готовый OTel-инструмент. Variadic-`opts` пробрасываются в соответствующий
конструктор meter'а как есть.

```go
func Counter(meter metric.Meter, domain, name string, opts ...metric.Int64CounterOption) metric.Int64Counter
func Histogram(meter metric.Meter, domain, name string, opts ...metric.Float64HistogramOption) metric.Float64Histogram
func Gauge(meter metric.Meter, domain, name string, opts ...metric.Int64UpDownCounterOption) metric.Int64UpDownCounter
func MustName(domain, name string) string
```

### Counter

Создаёт `Int64Counter` с именем `biz_<domain>_<name>_total`. Для монотонных
счётчиков событий.

### Histogram

Создаёт `Float64Histogram` с именем `biz_<domain>_<name>_seconds`. Суффикс
`_seconds` фиксирован — kit рассчитан на длительности в секундах.

### Gauge

Создаёт `Int64UpDownCounter` с именем `biz_<domain>_<name>` (без суффикса). Это
способ выразить gauge для значений, которые приложение пишет само (Add с
положительной/отрицательной дельтой). Для observable-источников (значение читается
коллбэком) используйте `meter.Int64ObservableGauge` напрямую, а имя соберите через
`MustName`.

### MustName

Собирает и валидирует имя `biz_<domain>_<name>` без создания инструмента. Нужен,
когда инструмент регистрируется вручную (ObservableGauge/Counter), но имя должно
остаться в конвенции. Panic-ует на невалидные `domain`/`name`.

## Инварианты и подводные камни

- **Panic при нарушении конвенции.** `domain` или `name` вне `^[a-z][a-z0-9_]*$`
  (заглавные буквы, дефис, пустая строка, первый символ — цифра) — `panic`. Это
  fail-fast: метрика с кривым именем не должна доехать до прода. Так как имена
  обычно регистрируются при старте сервиса, нарушение всплывёт сразу.

- **Panic при ошибке создания инструмента.** Если meter вернул ошибку на
  `Int64Counter`/`Float64Histogram`/`Int64UpDownCounter`, helper тоже паникует с
  именем метрики в сообщении — невалидная регистрация не маскируется.

- **Не собирать имя в обход helper'а.** Регистрация OTel-инструмента напрямую с
  произвольной строкой ломает конвенцию `biz_*`: метрику не подхватит автодискавер
  дашборда `Apps · Business`. Любую biz-метрику заводите через `Counter`/`Histogram`/
  `Gauge`, а ручные случаи — через `MustName`.

- **Префикс/суффикс не дублировать.** В `domain`/`name` нельзя класть `biz_`,
  `_total`, `_seconds` — их подставляет helper. `_` внутри `name` допустим
  (`login_attempts`), это часть snake_case.

- **`domain` vs `name`.** `domain` — бизнес-область (`auth`, `checkout`,
  `onboarding`); `name` — конкретное событие/величина (`login_attempts`,
  `orders_completed`, `active_carts`). Оба — обязательные, оба валидируются.

- **Конфликт типа на одном имени.** Имя метрики в OTel уникально в пределах
  meter'а: зарегистрировать одно и то же `biz_<domain>_<name>` и как Counter, и
  как другой инструмент нельзя — meter вернёт ошибку, helper её превратит в panic.
