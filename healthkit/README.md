# healthkit

Именованная health-проба. Киты с внешними зависимостями (`pgkit`, `natskit`,
`cachekit`, `storagekit/minio`) возвращают `healthkit.Probe`, `httpkit` собирает
пробы из fx-группы `healthkit.probes` и отдаёт `503` на `/ready`, если хоть одна вернула
ошибку.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/healthkit"
```

Версионирование и установка модуля — в [корневом README](../README.md)
(§ Версионирование, § Стабильность API).

## Быстрый старт

```go
fx.New(
    pgkit.Module, natskit.Module(""), cachekit.Module, httpkit.Module,
    healthkit.ProvideProbe(pgkit.Probe),
    natskit.ProvideProbe(""),
    healthkit.ProvideProbe(cachekit.Probe),
)
```

Своя проба — имя плюс проверка:

```go
healthkit.ProvideProbe(func(s *Sync) healthkit.Probe {
    return healthkit.New("hub-sync", func(ctx context.Context) error { return s.Lag(ctx) })
})
```

Именованный инстанс зависимости — через аннотации `fx` (у natskit — готовая
`natskit.ProvideProbe("events")`):

```go
healthkit.ProvideProbe(newSyncProbe, fx.ParamTags(`name:"events"`))
```

## Конфигурация

Конфига нет: `healthkit` — типы пробы и её panic, константа группы и четыре функции
без состояния и без fx-`Module`.
Таймаут пробы на `/ready` задаёт `httpkit`, применяя [`WithTimeout`](#withtimeout).

## API

```go
const ProbesGroup = "healthkit.probes"

type Probe struct {
    Name  string
    Check func(ctx context.Context) error
}

func New(name string, check func(ctx context.Context) error) Probe
func (p Probe) Run(ctx context.Context) error
func WithTimeout(p Probe, d time.Duration) Probe
func ProvideProbe(ctor any, anns ...fx.Annotation) fx.Option

type PanicError struct { // panic проверки
    Probe string
    Value any    // значение panic
    Stack []byte // стек горутины проверки
}
```

### Probe

`Name` — идентификатор зависимости в логе `/ready` (`pgkit`, `natskit:events`,
`storagekit`): имя кита, у инстанса — `<kit>:<instance>`, как имя его `fx.Module`. `Check` — сама проверка; ошибку она префиксует именем своего
кита (`pgkit: ping: …`), чтобы текст в логе был читаем и без `Name`.

### Run

Выполняет `Check` и возвращает его ошибку как есть. `Probe` с `Check == nil`
возвращает ошибку с именем пробы — обработчик `/ready` не паникует. panic проверки
`Run` возвращает как `*PanicError` (имя пробы, значение, стек): `/ready` и
health-loop `grpckit` зовут пробы в своих горутинах, где panic уронил бы процесс.

### WithTimeout

Ограничивает время одной проверки: `Check` получает `ctx` с дедлайном, а если
проверка `ctx` игнорирует — `Run` всё равно возвращает управление с ошибкой,
обёрнутой вокруг `context.DeadlineExceeded`. `d <= 0` возвращает пробу без
изменений. Имя пробы сохраняется; panic проверки — `*PanicError`, как у `Run`.

### ProvideProbe

`fx.Provide` с аннотацией `fx.ResultTags` в группу `healthkit.probes`. Конструктор должен возвращать
`healthkit.Probe` первым результатом (вторым допустим `error`); любой другой тип
отвергается при сборке графа — `fx.New(...).Err()` возвращает ошибку с текстом
`healthkit: ProvideProbe …`.

Дополнительные аннотации (`fx.ParamTags` для именованного инстанса, `fx.As`)
применяются до групповой. Свой `fx.ResultTags` передавать нельзя: групповой тег
ставит сам `ProvideProbe` — иначе проверка типа обходится, а проба молча не
попадает в группу.

## Observability

Своих метрик и спанов у кита нет. Результат проб виден в `httpkit`: код ответа
`/ready`, тело со статусом каждой пробы по имени и warn-лог
`readiness probe failed` с полями `probe` (значение `Probe.Name`) и `err`; panic
пробы — error-лог `readiness probe panicked` с `probe`, `panic` и `stack`.

## Инварианты

- **Агрегация проб — не здесь.** `healthkit` не запускает набор проб: `httpkit`
  прогоняет все пробы группы, логирует каждую упавшую и отвечает `503`, если
  упала хотя бы одна. Второго контракта с другой семантикой кит не заводит.
- **Конструктор возвращает именно `healthkit.Probe`.** dig ключует fx-группу
  парой «имя + тип»: значение другого типа в группе `healthkit.probes` `httpkit` не увидит.
  `ProvideProbe` проверяет тип конструктора и валит сборку графа, а не прод.
- **Проба не должна быть дорогой.** `/ready` дёргают оркестраторы каждые
  несколько секунд; используйте `PING`/`SELECT 1`, не бизнес-запросы.
- **Проба обязана уважать `ctx`.** `WithTimeout` вернёт управление и без этого,
  но горутина зависшей проверки живёт до её собственного завершения.
- **Текст ошибки не уходит наружу.** `httpkit` логирует ошибку пробы и отвечает
  `503` с телом `{"status":"unhealthy","probes":[{"name","status"}]}` — имена и
  статусы проб, без текста ошибки: в нём бывают DSN и внутренние адреса.
