# timerkit

Periodic jobs через fx group. Сервис регистрирует `Job` в группу `timer.job`, а
`timerkit.Module` подхватывает их в `fx.Lifecycle`: на старте валидирует и
запускает каждую в своей горутине, на остановке отменяет контекст и **дожидается
завершения** идущих запусков в пределах дедлайна остановки. Panic внутри job'а
перехватывается и логируется; каждый запуск учитывается в метриках.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/timerkit"
```

## Быстрый старт

```go
func NewCleanupJob(svc *Service) timerkit.Job {
	return timerkit.Job{
		Name:       "expired-sessions-cleanup",
		Interval:   5 * time.Minute,
		Timeout:    time.Minute,        // дедлайн ctx одного запуска
		Jitter:     30 * time.Second,   // случайная добавка к ожиданию
		RunOnStart: true,               // первый запуск сразу
		Run:        svc.PurgeExpiredSessions,
	}
}

var Module = fx.Module("cleanup",
	fx.Provide(
		NewService,
		fx.Annotate(NewCleanupJob, fx.ResultTags(`group:"timer.job"`)),
	),
)
```

В приложении подключается `timerkit.Module` рядом с модулем, регистрирующим
job. Логи идут через `logkit.Logger` из графа:

```go
fx.New(
	fx.Provide(logkit.NewConfig),
	logkit.Module,      // → logkit.Logger
	timerkit.Module,    // подхватывает все group:"timer.job"
	cleanup.Module,
).Run()
```

### Жизненный цикл

- **OnStart** — валидирует все job'ы (ошибка называет job и валит старт) и
  поднимает горутину на каждую.
- **Ожидание** отсчитывается от завершения предыдущего запуска: `Interval` +
  случайный `[0, Jitter)`. Запуски одной job не перекрываются.
- **OnStop** — отменяет общий `ctx` (он же передаётся в `Run`) и ждёт выхода
  всех горутин; если дедлайн `OnStop` истёк раньше — возвращает ошибку
  `timerkit: stop: jobs still running`.

## Конфигурация

Конфига нет — ENV-переменных кит не читает; всё задаётся полями `Job`.

## Observability

Метрики через `otel.Meter("git.stit.tech/stit-core/golibinfra/timerkit")`
(no-op без `MeterProvider`):

| Метрика | Тип | Labels | Что |
| --- | --- | --- | --- |
| `timer_job_runs_total` | counter | `job`, `result` (`ok` / `error` / `panic`) | запуски `Run` по исходу |
| `timer_job_duration_seconds` | histogram (s) | `job` | длительность одного запуска |

Логи (`logkit.Logger`, уровень error):

| Событие | Сообщение | Атрибуты |
| --- | --- | --- |
| `Run` вернул ошибку | `timerkit: job error` | `job`, `error` |
| Panic внутри `Run` | `timerkit: panic in job` | `job`, `panic`, `stack` |

Успешные запуски не логируются. Спанов кит не эмитит — оборачивайте тело
`Run` в `telemetry.Span`, если нужен трейс (корневой спан стартует от
глобального провайдера).

## API

```go
type Job struct {
	Name       string
	Interval   time.Duration
	Run        func(ctx context.Context) error
	Timeout    time.Duration // дедлайн ctx одного запуска; 0 — без дедлайна
	Jitter     time.Duration // случайная добавка [0, Jitter) к ожиданию; 0 — без джиттера
	RunOnStart bool          // первый запуск сразу на старте, а не через Interval
}

var ErrJob error   // невалидная Job (обёрнут в ошибку OnStart)
var Module fx.Option
```

| Поле | Роль |
| --- | --- |
| `Name` | Идентификатор в логах и label `job` метрик. Обязателен и уникален в пределах приложения. |
| `Interval` | Пауза между запусками, `> 0`. |
| `Run` | Тело задачи; обязателен. Ошибка логируется и не прерывает цикл. |
| `Timeout` | `>= 0`. При `> 0` каждый запуск получает `context.WithTimeout`. |
| `Jitter` | `>= 0`. Размазывает запуски однотипных job'ов между инстансами. |
| `RunOnStart` | Прогон на старте (в горутине job, старт приложения не блокирует). |

Регистрация — только через fx-группу `timer.job`; вне `fx.App` публичного API
запуска нет.

## Тестирование

Тесты кита не спят: runner принимает управляемый таймер (тест сам «тикает»), а
метрики читаются через `sdkmetric.ManualReader`. fx-уровень — `fx.New` +
`Start`/`Stop`: остановка ждёт идущий запуск, ошибка валидации называет job,
дедлайн `Stop` соблюдается при `Run`, игнорирующем `ctx`.

Свою периодику тестируйте на уровне `Run` — это обычная `func(ctx) error`.

## Инварианты и подводные камни

- **Тег группы — ровно `group:"timer.job"`.** Опечатка не даёт ошибки
  компиляции — job молча не попадёт в группу.
- **Валидация — на старте.** `Name == ""`, `Run == nil`, `Interval <= 0`,
  отрицательные `Timeout`/`Jitter`, дубль имени → `OnStart` возвращает
  `ErrJob` с именем job, приложение не стартует.
- **`Run` обязан уважать `ctx`.** На остановке ctx отменяется, но идущий `Run`
  не убивается: `OnStop` ждёт его до дедлайна остановки, затем возвращает
  ошибку. `Timeout` даёт запуску собственный дедлайн.
- **Ожидание — от конца предыдущего запуска**, поэтому фактический период =
  `Interval` + длительность `Run` (+ jitter). Пропущенных тиков и наложений нет.
- **Panic не валит процесс**, но это страховка, а не обработка ошибок:
  возвращайте `error` из `Run`.
- **Backoff/retry/отключения «сломанной» job у кита нет** — реализуйте внутри `Run`.
