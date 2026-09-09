# clockkit

Port для системных часов и его production-реализация (`Real`, поверх пакета
`time`). Сервис зависит от интерфейса `Clock`, а не от `time.Now()` и
`time.After()` напрямую — тогда в тестах вместо реального времени подставляются
детерминированные фейки `Fixed` (застывший момент) и `Tickable` (ручное
продвижение часов, будящее таймеры). Фейки лежат в этом же пакете, чтобы не
плодить отдельный `clocktest`.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/clockkit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

Сервис принимает `clockkit.Clock` и берёт время только через него:

```go
package billing

import (
	"context"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

type Service struct {
	clock clockkit.Clock
}

func NewService(clock clockkit.Clock) *Service {
	return &Service{clock: clock}
}

func (s *Service) IsExpired(deadline time.Time) bool {
	return s.clock.Now().After(deadline)
}

// Ожидание — тоже через часы: в тесте оно не съест реальные 30 секунд.
func (s *Service) WaitForWindow(ctx context.Context, d time.Duration) error {
	timer := s.clock.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return nil
	}
}
```

В приложении часы приходят из fx — `clockkit.Module` провайдит `Clock` как
`Real{}`:

```go
fx.New(
	clockkit.Module,    // provide Clock = Real{}
	fx.Provide(NewService),
).Run()
```

В тесте вместо `Real` подставляется фейк. `Fixed` — когда время должно быть
ровно одним и тем же на всём прогоне:

```go
clock := clockkit.NewFixed(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
svc := NewService(clock)
// svc.clock.Now() всегда вернёт 2026-01-02T03:04:05Z
```

`Tickable` — когда тест должен явно «двигать» время и наблюдать поведение до и
после, в том числе будить ожидающие таймеры:

```go
clock := clockkit.NewTickable(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
svc := NewService(clock)

if svc.IsExpired(deadline) {
	t.Fatal("not expired yet")
}
clock.Advance(24 * time.Hour) // прыжок на сутки вперёд
if !svc.IsExpired(deadline) {
	t.Fatal("expired after a day")
}
```

```go
done := make(chan error, 1)
go func() { done <- svc.WaitForWindow(ctx, 30*time.Second) }()

clock.WaitTimers(1)          // горутина уже поставила таймер
clock.Advance(30 * time.Second) // и он срабатывает здесь же, без sleep
if err := <-done; err != nil {
	t.Fatal(err)
}
```

## Конфигурация

Конфига нет — у кита нет `Config`-структуры и ENV-переменных. `Real` не
параметризуется (всегда пакет `time`), фейки настраиваются аргументами
конструкторов (`NewFixed`/`NewTickable`).

## Observability

Кит ничего не эмитит — ни метрик, ни спанов, ни логов, и не зависит от
OTel. Это чистый примитив над `time`.

## API

```go
type Clock interface {
	Now() time.Time                          // текущий момент
	Since(t time.Time) time.Duration         // сколько прошло от t
	After(d time.Duration) <-chan time.Time  // канал срабатывания через d
	NewTimer(d time.Duration) Timer          // таймер, который можно снять и перезапустить
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool                  // false — таймер уже сработал или снят
	Reset(d time.Duration) bool  // false — таймер уже сработал или снят
}
```

| Тип | Конструктор | Роль | Когда |
| --- | --- | --- | --- |
| `Real` | `Real{}` | `time.Now`/`time.Since`/`time.After`/`time.NewTimer` | Production. Провайдится `Module` по умолчанию. |
| `Fixed` | `NewFixed(t time.Time)` | `Now()` всегда возвращает один и тот же `t`; таймеры не срабатывают | Тест, где время должно быть фиксированным и не меняться. |
| `Tickable` | `NewTickable(start time.Time)` | `Now()` двигается вручную, `Advance`/`Set` будят таймеры | Тест, где нужно симулировать ход времени и ожидания. |

`Tickable` сверх `Clock` даёт:

- `Advance(d time.Duration)` — сдвинуть текущее значение на `d` вперёд.
- `Set(t time.Time)` — установить текущее значение в `t` (в т.ч. в прошлое).
- `WaitTimers(n int)` — заблокироваться, пока на часах не окажется `n`
  **ожидающих** таймеров: сработавший из очереди уходит, поэтому счёт идёт по
  ещё не наступившим дедлайнам, а не по когда-либо поставленным таймерам.

Все три типа реализуют `Clock`. `Tickable` потокобезопасен: состояние часов и
очередь таймеров под одним мьютексом, конкурентные `Advance` суммируются без
гонок, срабатывание происходит **внутри** `Advance`/`Set` — после возврата из
них тест уже может читать канал.

`clockkit.Module` — fx-модуль, провайдит `Clock` как `Real{}`. Фейки во fx-граф
не отдаются — они подставляются напрямую в тестах (или через `fx.Supply` в
тестовом графе).

## Тестирование

Детерминизм достигается за счёт того, что сервис нигде не зовёт `time.Now()` и
`time.After()` сам, а только методы `Clock`. В production туда приходит `Real`,
в тесте — фейк:

- `Fixed` фиксирует момент: сколько бы раз ни вызвали `Now()`, ответ один и тот
  же. Подходит для проверки форматирования, сравнения с фиксированным
  дедлайном, снапшотов. Таймер с `d > 0` на нём не сработает никогда — время не
  движется.
- `Tickable` стартует с `start` и стоит на месте, пока тест явно не позовёт
  `Advance` (сдвиг на длительность) или `Set` (установка в произвольный момент).
  Подходит для сценариев «истёк TTL», «прошёл интервал», «окно закрылось» — без
  реальных `time.Sleep`.
- `WaitTimers(n)` снимает гонку между горутиной, которая ставит таймер, и
  тестом, который двигает часы: без него `Advance` может пройти раньше
  регистрации, и тест повиснет на канале. Считаются ожидающие таймеры, а
  сработавший очередь покидает: `WaitTimers(1)` после `Advance` ждёт уже
  **следующий** таймер цикла, а не тот, что только что сработал.

### Kit'ы, которые принимают часы

Кит, чьё поведение зависит от времени (истёк ли токен, какой месяц партиции,
когда следующий тик), принимает `Clock` и не зовёт `time.Now()` сам:

- `timerkit` — берёт `Clock` из графа (опционально, иначе `Real{}`) и считает по
  нему ожидание между запусками;
- `authnkit`, `cachekit` (`MemoryCache`) — через опцию `WithClock(clock)` у
  конструктора;
- `amocrmkit` — аргументом конструктора `New`.

```go
// Паттерн опции: конструктор берёт Real{}, тест подменяет фейком.
type Option func(*Service)

func WithClock(c clockkit.Clock) Option { return func(s *Service) { s.clock = c } }

func New(opts ...Option) *Service {
	s := &Service{clock: clockkit.Real{}}
	for _, o := range opts {
		o(s)
	}
	return s
}
```

Замер длительности (`Since`) — тоже через `Clock`: тогда гистограмма в тесте
детерминирована.

## Инварианты и подводные камни

- **Не зовите `time.Now()`/`time.After()` в обход `Clock`.** Любой прямой вызов
  в коде сервиса ломает детерминизм теста — фейк его не перехватывает.
- **`Fixed` действительно застывает.** Между двумя `Now()` нуля разницы — это
  не «почти сейчас», а ровно `t`. Если тесту нужно увидеть ход времени, берите
  `Tickable`.
- **`Tickable` сам не идёт.** Время стоит, пока тест не позовёт `Advance`/`Set`.
  `Set` может вернуть часы в прошлое — `Clock` монотонность не гарантирует, и
  таймеры при движении назад не срабатывают.
- **`Timer` повторяет семантику `time.Timer`.** Канал ёмкостью один, `Stop`
  и `Reset` возвращают «таймер был активен»; у фейков `Reset` дополнительно
  очищает канал, чтобы старое срабатывание не прилетело после перезапуска.
- **Таймзона — это `time.Time` в аргументе.** Кит ничего не нормализует и не
  переводит в UTC: `Real` возвращает локальное время как `time.Now()`, фейки —
  ровно тот `Time`, что вы передали. Сравнивайте моменты через `Equal`, а зону
  фиксируйте на стороне теста (обычно `time.UTC`); границы суток — `tzkit`.
- **`Now()` отдаёт wall-clock-момент, `Since` — интервал.** У `Real` это
  `time.Since` с монотонными часами рантайма, у фейков — разность их значений.
