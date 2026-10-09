# config

Загрузчик **App-конфига приложения**: структура, которая собирает конфиги китов
в поля, заполняется композицией источников (`Source`) — штатно из переменных
окружения. Источник правды по поведению — код пакета; сводная строка в общей
карте — [`../README.md` § Пакеты](../README.md#пакеты).

## Назначение

Кит golibinfra читает своё окружение сам: `<kit>.NewConfig()` зовёт ридер ENV
китов напрямую и **проходит мимо** `config.Source`. `config.Env` читает тем же
ридером — с той же семантикой имён и пустых значений. `config` нужен там, где
приложение хочет одну точку загрузки: App-структуру с конфигами китов в полях,
единый порядок приоритета источников и возможность добавить свой `Source` без
правки китов.

Отсюда граница: кастомный `Source` влияет только на структуры, переданные в
`config.Load`. Если кит подключён через `fx.Provide(<kit>.NewConfig)`, его
значения придут из ENV мимо источников.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/config"
```

Версию модуля не дублируем здесь — она в [`../README.md` § Использование в
проекте](../README.md#использование-в-проекте).

## Быстрый старт

App-структура держит конфиги китов полями, `Load` заполняет её, fx-провайдеры
раздают поля китам:

```go
package appconfig

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/config"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

type App struct {
	Postgres pgkit.PoolConfig
	HTTP     httpkit.Config
}

func Load(srcs []config.Source) (*App, error) {
	var cfg App
	if err := config.Load(&cfg, srcs...); err != nil {
		return nil, err
	}
	return &cfg, nil
}

var Module = fx.Module("appconfig",
	fx.Provide(
		Load,

		func(a *App) pgkit.PoolConfig { return a.Postgres },
		func(a *App) httpkit.Config { return a.HTTP },
	),
)
```

Приложение подключает источники и свой App-модуль:

```go
fx.New(
    config.Module, // env → []config.Source
    appconfig.Module,
    fx.Provide(logkit.NewConfig), // конфиг кита вне App — его конструктором
    logkit.Module,
    pgkit.Module,
    httpkit.Module,
)
```

Загрузка конфига идёт на сборке графа и ctx не берёт: ctx в графе дал бы любому
конструктору вечный фоновый ctx вместо ctx запроса или `OnStart`.

Набор источников можно задать и без `config.Module` — например, добавив свой
`Source` после env:

```go
fx.Provide(func(own MySource) []config.Source {
	return []config.Source{config.Env(), own} // own — своя реализация config.Source
})
```

### Без fx

```go
var cfg App
if err := config.Load(&cfg, config.Env()); err != nil {
	return err
}
```

Точные сигнатуры — см. [§ API](#api).

## Конфигурация

`config` собственных ENV-переменных не читает: он прогоняет через ридер ENV китов
структуры, которые ему передали. Имена и типы переменных задаёт каждый кит
своими `envconfig`-тегами (их стабильность и правила ридера — [`../README.md` §
Стабильность API](../README.md#стабильность-api)).

### Имена переменных

Имя переменной — `envconfig`-тег поля. Вложенная структура (без своего разбора)
читается теми же именами, приставки имени поля нет; тег на вложенной структуре — ошибка
`Load` с именем поля. Для App-структуры из примера выше:

```
Postgres pgkit.PoolConfig → DSN `envconfig:"PG_DSN"` → PG_DSN
HTTP     httpkit.Config   → Addr `envconfig:"HTTP_ADDR"` → HTTP_ADDR
```

Поле без тега из ENV не читается: если оно не помечено `ignored:"true"`, `Load`
возвращает ошибку с именем поля. `EnvPrefixed(prefix)` читает `PREFIX_<тег>`, а
при незаданной — `<тег>`.

Две секции одного кита без тегов читают одни переменные. Вторую секцию помечают
`ignored:"true"` и читают отдельным источником с приставкой:

```go
type App struct {
	Primary   pgkit.PoolConfig                  // PG_*
	Analytics pgkit.PoolConfig `ignored:"true"` // ANALYTICS_PG_*
}

var cfg App
if err := config.Load(&cfg, config.Env()); err != nil {
	return err
}
if err := config.Load(&cfg.Analytics, config.EnvPrefixed("analytics")); err != nil {
	return err
}
```

Незаданная `ANALYTICS_<тег>` берётся из `<тег>`: без своих обязательных переменных вторая
секция молча читает значения первой, поэтому их задают явно.

Тип поля — строка, bool, число, `time.Duration`, список из них или тип с `Decode` либо
`UnmarshalText`. Поле `map`, `[]byte`, `url.URL` и другого типа только с `UnmarshalBinary`
или `Set` — ошибка `Load` с именем поля, заданная переменная или нет: такое значение
читают строкой с разбором в `Validate` или типом с `UnmarshalText`.

Пустое значение (`KEY=`) — как незаданная переменная: берётся `default`-тег, без
него поле не меняется. Пустую строку, пустой список или пустое значение типа со своим
разбором задаёт значение `-`; список из одних разделителей (`,`) — ошибка. Целые —
десятичные. Неразбираемое значение — ошибка с именем переменной.

## Observability

Кит **ничего не эмитит**: ни метрик, ни спанов, ни логов — это разбор
переменных окружения в структуру, без I/O и фонового состояния.

## API

### `Source`

```go
type Source interface {
	Apply(dst any) error
}
```

Источник, заполняющий переданную структуру `dst` (указатель на struct). Штатная
реализация — `Env`/`EnvPrefixed`. Своя реализация (файл, remote)
подставляется в `[]Source` наравне с ними; порядок в срезе — порядок приоритета.
Источник с I/O сам ограничивает своё время (таймаут клиента, дедлайн чтения):
`Load` зовут на сборке графа, где ctx запроса нет.

### `Load`

```go
func Load(dst any, sources ...Source) error
```

Прогоняет `dst` через каждый `Source` по порядку; поздний источник перезатирает
поля раннего. Применяются **все** источники, их ошибки собираются в одну через
`errors.Join`; каждая обёрнута индексом своего источника (`config: source[i]: …`)
и достижима через `errors.Is`/`errors.As`; ошибка ENV-источника называет
переменную (`config: source[0]: WORKERS: …`). Без источников — no-op, `dst` не
меняется.

### `Env`, `EnvPrefixed`

```go
func Env() Source
func EnvPrefixed(prefix string) Source
```

Источники из переменных окружения — ридером ENV китов ([§ Имена
переменных](#имена-переменных)). `EnvPrefixed` приводит `prefix` к верхнему
регистру и склеивает с тегом через `_`; при незаданной `PREFIX_<тег>` значение
берётся из `<тег>`.

### `Module`

```go
var Module = fx.Module("config", …)
```

Провайдит в fx-граф `[]Source{Env()}`. Нужен другой набор источников — не
подключайте `Module`, а провайдите `[]config.Source` сами.

## Тестирование

- **ENV-источник** тестируется выставлением переменных окружения (`t.Setenv`) и
  вызовом `config.Load(&cfg, config.Env())` с проверкой заполненных полей.
- **Свой `Source`** — функциональный тип в две строки:
  `type sourceFunc func(dst any) error` с методом `Apply`.

Запуск:

```bash
go test -short -race ./config/...
```

## Инварианты и подводные камни

- **Приоритет = порядок в `Load`.** Источники применяются слева направо, поздний
  перезатирает раннего.
- **Ошибки не останавливают загрузку.** Отработают все источники, вернётся
  объединённая ошибка; при ошибке `dst` считается негодным целиком.
- **ENV — только по `envconfig`-тегам.** Поле без тега — ошибка `Load` (явный
  отказ — `ignored:"true"`); вложенная структура — без тега, читается теми же именами, без
  приставки имени поля, вторая секция того же кита — отдельным `Load` с `EnvPrefixed` (см.
  [§ Имена переменных](#имена-переменных)).
- **Пустое значение — дефолт.** `KEY=` поле не обнуляет; пустую строку или список
  задаёт `-`, список из одних разделителей — ошибка.
- **Киты не зависят от `Source`.** `<kit>.NewConfig` читает ENV напрямую;
  кастомный источник действует только на структуры, переданные в `Load`.
