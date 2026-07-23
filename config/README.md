# config

Единый загрузчик конфигурации для китов golibinfra: значения из переменных
окружения через композицию источников. Источник правды по поведению — код
пакета; сводная строка в общей карте — [`../README.md` § Пакеты](../README.md#пакеты).

## Назначение

Каждый кит держит свою `Config`-структуру с `envconfig`-тегами. `config` не знает
про эти структуры — он даёт композицию **источников** (`Source`), которые умеют
заполнять *любую* переданную структуру:

- `Env(prefix…)` — читает значения из переменных окружения по `envconfig`-тегам.

`Load(ctx, dst, sources…)` прогоняет `dst` через каждый источник по порядку.
Поздние источники перезатирают поля более ранних — это и есть контракт
приоритета. Модель источников оставляет место для дополнительных реализаций
`Source` без изменения кода китов.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/config"
```

Версию модуля не дублируем здесь — она в [`../README.md` § Использование в
проекте](../README.md#использование-в-проекте).

## Быстрый старт

### Через fx

`config.Module` провайдит в граф `[]config.Source`. Кит зависит от
`[]config.Source` и грузит свою структуру через `config.Load`:

```go
package authnkit

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/config"
	"go.uber.org/fx"
)

type Config struct {
	Issuer    string `envconfig:"JWT_ISSUER" default:"stit"`
	JWTSecret string `envconfig:"JWT_SECRET"`
}

func newConfig(srcs []config.Source) (Config, error) {
	var c Config
	if err := config.Load(context.Background(), &c, srcs...); err != nil {
		return c, err
	}
	return c, nil
}

var Module = fx.Module("authnkit",
	fx.Provide(newConfig),
	// …
)
```

Приложение собирает граф из обоих модулей:

```go
fx.New(
	config.Module,   // env → []config.Source
	authnkit.Module, // потребляет []config.Source
)
```

### Без fx

Источники можно собрать руками — `Module` ничего магического не делает:

```go
srcs := []config.Source{config.Env()}

var cfg MyConfig
if err := config.Load(ctx, &cfg, srcs...); err != nil {
	return err
}
```

Точные сигнатуры — см. [§ API](#api).

## Конфигурация

`config` собственных ENV-переменных не читает — он лишь прогоняет структуры китов
через `envconfig`. Имена и типы переменных задаёт каждый кит своими
`envconfig`-тегами (их стабильность — [`../README.md` § Стабильность
API](../README.md#стабильность-api)).

## API

### `Source`

```go
type Source interface {
	Apply(ctx context.Context, dst any) error
}
```

Источник, который заполняет переданную структуру `dst` (всегда указатель на
struct). Штатная реализация — `Env`. Кит работает не с конкретной реализацией, а
со срезом `[]Source` — это и есть точка композиции.

### `Load`

```go
func Load(ctx context.Context, dst any, sources ...Source) error
```

Прогоняет `dst` через каждый `Source` по порядку. Поздний источник перезатирает
поля раннего. Ошибка любого источника оборачивается с его индексом
(`config: source[i]: …`) и прерывает загрузку. Это основной вызов внутри
конструктора `Config` каждого кита.

### `Env`

```go
func Env(prefix ...string) Source
```

Источник из переменных окружения через `envconfig`. Необязательный `prefix`
добавляет приставку к именам переменных (передаётся в `envconfig.Process`); без
аргумента — без префикса. В большинстве китов используется как `config.Env()`.

### `Module`

```go
var Module = fx.Module("config", …)
```

Провайдит в fx-граф `[]Source{Env()}`. Кит подключает `config.Module` и зависит
от `[]config.Source`.

## Тестирование

- **ENV-источник** тестируется выставлением переменных окружения (`t.Setenv`) и
  вызовом `config.Load(ctx, &cfg, config.Env())` с проверкой заполненных полей.

Запуск:

```bash
go test -short -race ./config/...
```

## Инварианты и подводные камни

- **Приоритет = порядок в `Load`.** Источники применяются слева направо, поздний
  перезатирает раннего.
- **ENV мапится по `envconfig`-тегам.** Поле без `envconfig`-тега из ENV не
  возьмётся.
