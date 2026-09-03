# healthkit

Общий тип health-пробы. Киты с внешними зависимостями (`pgkit`, `natskit`,
`cachekit`) возвращают `healthkit.Probe`, `httpkit` собирает пробы из fx-группы
`health` и отдаёт `503` на `/ready`, если хоть одна вернула ошибку.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/healthkit"
```

## Быстрый старт

```go
fx.New(
    pgkit.Module, natskit.Module(""), cachekit.Module, httpkit.Module,
    healthkit.Provide(pgkit.DBProbe),
    healthkit.Provide(natskit.Probe),
    healthkit.Provide(cachekit.Probe),
)
```

Своя проба — любая функция `func(ctx) error`, приведённая к `healthkit.Probe`:

```go
healthkit.Provide(func(s *Sync) healthkit.Probe {
    return func(ctx context.Context) error { return s.Lag(ctx) }
})
```

## API

```go
type Probe func(ctx context.Context) error

const Group = "health"

func Provide(ctor any) fx.Option          // fx.Provide + ResultTags(`group:"health"`)
func All(ctx context.Context, probes ...Probe) error
```

## Инварианты

- **Именованный тип обязателен.** dig ключует fx-группу парой «имя + тип»:
  безымянный `func(ctx) error`, положенный в группу `health`, `httpkit` не увидит.
  Конструктор для `Provide` должен возвращать именно `healthkit.Probe`.
- **Проба не должна быть дорогой.** `/ready` дёргают оркестраторы каждые
  несколько секунд; используйте `PING`/`SELECT 1`, не бизнес-запросы.
- **Текст ошибки не уходит наружу.** `httpkit` логирует ошибку пробы и отвечает
  общим `503 unhealthy`, чтобы DSN и внутренние адреса не попадали в ответ.
