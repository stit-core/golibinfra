# config

Загрузчик **App-конфига приложения**: структура, которая собирает конфиги китов
в поля, заполняется композицией источников (`Source`) — штатно из переменных
окружения. Источник правды по поведению — код пакета; сводная строка в общей
карте — [`../README.md` § Пакеты](../README.md#пакеты).

## Назначение

Кит golibinfra читает своё окружение сам: `<kit>.NewConfig()` вызывает
`envconfig.Process` напрямую и **проходит мимо** `config.Source`. `config` нужен
там, где приложение хочет одну точку загрузки: App-структуру с конфигами китов
в полях, единый порядок приоритета источников и возможность добавить свой
`Source` без правки китов.

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

`config` собственных ENV-переменных не читает: он прогоняет через `envconfig`
структуры, которые ему передали. Имена и типы переменных задаёт каждый кит
своими `envconfig`-тегами (их стабильность — [`../README.md` § Стабильность
API](../README.md#стабильность-api)).

### Имена переменных

Для поля с `envconfig`-тегом имя переменной — сам тег; для поля без тега — имя
поля в верхнем регистре. Вложенная структура (без своего декодера) добавляет
приставку — **имя поля**, в верхнем регистре, через `_`. Для App-структуры из
примера выше:

```
Postgres pgkit.PoolConfig → DSN `envconfig:"PG_DSN"` → POSTGRES_PG_DSN, затем PG_DSN
HTTP     httpkit.Config   → Addr `envconfig:"HTTP_ADDR"` → HTTP_HTTP_ADDR, затем HTTP_ADDR
```

То есть у поля с тегом префиксованное имя имеет приоритет, а тег работает как
запасное имя. Случайно заданная переменная с приставкой имени поля молча
перекроет привычную: POSTGRES_PG_DSN победит PG_DSN.

`EnvPrefixed(prefix)` добавляет свою приставку тем же правилом — поверх приставок
вложенных структур.

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
и достижима через `errors.Is`/`errors.As` (в том числе
`*envconfig.ParseError`). Без источников — no-op, `dst` не меняется.

### `Env`, `EnvPrefixed`

```go
func Env() Source
func EnvPrefixed(prefix string) Source
```

Источники из переменных окружения через `envconfig`. `EnvPrefixed` приводит
`prefix` к верхнему регистру и склеивает с именем через `_`; при отсутствии
PREFIX_NAME значение берётся из непрефиксованного NAME — но только для полей
с `envconfig`-тегом (у поля без тега запасного имени нет).

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
- **ENV мапится по `envconfig`-тегам.** Поле без `envconfig`-тега читается по
  имени поля в верхнем регистре и не имеет запасного (непрефиксованного) имени.
- **Имя поля вложенной структуры — приставка.** Переменная с приставкой имеет
  приоритет над именем из тега (см. [§ Имена переменных](#имена-переменных)).
- **Киты не зависят от `Source`.** `<kit>.NewConfig` читает ENV напрямую;
  кастомный источник действует только на структуры, переданные в `Load`.
