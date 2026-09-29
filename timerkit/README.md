# timerkit

Periodic jobs через fx-группу. Сервис регистрирует `Job` через
`timerkit.Provide`, а `timerkit.Module` подхватывает группу в `fx.Lifecycle`: на
старте валидирует и запускает каждую job в своей горутине, на остановке отменяет
контекст и **дожидается завершения** идущих запусков в пределах дедлайна
остановки. Каждый запуск — свой root-спан; panic внутри job'а перехватывается и
логируется; исход и длительность учитываются в метриках.

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
	fx.Provide(NewService),
	timerkit.Provide(NewCleanupJob), // конструктор Job или []Job → группа timerkit.Group
)
```

В приложении подключается `timerkit.Module` рядом с модулем, регистрирующим
job: `timerkit.Provide` требует его в графе, и без него сборка падает с
`missing type: timerkit.Scheduler` — job, которую никто не запускает, не бывает
молчаливой. Логи идут через `logkit.Logger` из графа, расписание — по
`clockkit.Clock`:

```go
fx.New(
	fx.Provide(logkit.NewConfig),
	logkit.Module,      // → logkit.Logger
	clockkit.Module,    // → clockkit.Clock (без него — Real{})
	timerkit.Module,    // подхватывает всю группу timerkit.Group
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

Единственная опциональная зависимость — `clockkit.Clock` из графа: по нему
считается ожидание между запусками и длительность запуска. Без `clockkit.Module`
берётся `clockkit.Real{}`; в тесте `clockkit.Tickable` в графе делает расписание
детерминированным. Дедлайн `Timeout` — это `context.WithTimeout`, он идёт по
реальному времени и от подменённых часов не зависит.

## Observability

Спан на каждый запуск через `otel.Tracer("git.stit.tech/stit-core/golibinfra/timerkit")`
(no-op без `TracerProvider`):

| Спан | Kind | Атрибуты | Статус |
| --- | --- | --- | --- |
| `timerkit.job <name>` | internal | `timer.job` = `Job.Name` | `Error` + `RecordError` на ошибке `Run`; `Error`/`panic` на panic'е |

Спан — корень трейса запуска: всё, что инструментировано внутри `Run` (pgkit,
httpclient, natskit), становится его детьми, а не сиротскими трейсами.

Метрики через `otel.Meter("git.stit.tech/stit-core/golibinfra/timerkit")`
(no-op без `MeterProvider`):

| Метрика | Тип | Labels | Что |
| --- | --- | --- | --- |
| `timer_job_runs_total` | counter | `job`, `result` (`ok` / `error` / `panic`) | запуски `Run` по исходу |
| `timer_job_duration_seconds` | histogram (s, общие бакеты `otelkit.DurationBuckets`) | `job` | длительность одного запуска |

Логи (`logkit.Logger`, уровень error):

| Событие | Сообщение | Атрибуты |
| --- | --- | --- |
| `Run` вернул ошибку | `timerkit: job error` | `job`, `error` |
| Panic внутри `Run` | `timerkit: panic in job` | `job`, `panic`, `stack` |

Успешные запуски не логируются; лог пишется внутри спана запуска, поэтому несёт
его trace-id.

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

const Group = "timerkit.jobs"                              // имя fx-группы job'ов
func Provide(ctor any, anns ...fx.Annotation) fx.Option     // конструктор Job или []Job → группа; требует Module
type Scheduler struct{}                                     // маркер Module в графе
var ErrJob error                                            // невалидная Job (обёрнута в ошибку OnStart)
var Module fx.Option                                        // запускает группу, provide'ит Scheduler
```

| Поле | Роль |
| --- | --- |
| `Name` | Идентификатор в логах, спанах и label `job` метрик. Обязателен и уникален в пределах приложения. |
| `Interval` | Пауза между запусками, `> 0`. |
| `Run` | Тело задачи; обязателен. Ошибка логируется и не прерывает цикл. |
| `Timeout` | `>= 0`. При `> 0` каждый запуск получает `context.WithTimeout`. |
| `Jitter` | `>= 0`. Размазывает запуски однотипных job'ов между инстансами. |
| `RunOnStart` | Прогон на старте (в горутине job, старт приложения не блокирует). |

`Provide` принимает конструктор, у которого **первый** результат — `Job` (тег
`group:"timerkit.jobs"`) или `[]Job` (тег `group:"timerkit.jobs,flatten"` — как в
`hubkit/sync`, где движки разворачиваются в набор job'ов); вторым результатом
может идти `error`. Конструктор с любой другой первой позицией (в том числе
`func() (error, Job)`) отвергается при сборке графа. Дополнительные
аннотации вызывающего (`fx.ParamTags` для параметра из другой группы)
передаются вторым аргументом:

```go
timerkit.Provide(jobsFromEngines, fx.ParamTags(`group:"hubkit.sync.engine"`))
```

Каждый `Provide` добавляет в граф `fx.Invoke`, которому нужен `timerkit.Scheduler`
— его даёт только `Module`. Поэтому модуль кита с job'ами (`outboxkit`,
`bruteforcekit/pgstore`, `hubkit/sync`) без `timerkit.Module` не собирается ни в
`fx.New`, ни в `fx.ValidateApp`, а повторное подключение `Module` — ошибка
`already provided`, а не двойной запуск job'ов.

Регистрация — только через fx-группу; вне `fx.App` публичного API запуска нет.

## Тестирование

Тесты кита не спят: часы — `clockkit.Tickable`, срабатывание таймера происходит
внутри `Advance`, а `WaitTimers` снимает гонку «горутина job ещё не поставила
таймер». Метрики читаются через `sdkmetric.ManualReader`, спаны — через
`tracetest.SpanRecorder`. fx-уровень — `fx.New` + `Start`/`Stop`: остановка ждёт
идущий запуск, ошибка валидации называет job, дедлайн `Stop` соблюдается при
`Run`, игнорирующем `ctx`.

Свою периодику тестируйте на уровне `Run` — это обычная `func(ctx) error`;
расписание вокруг неё проверяется подстановкой `clockkit.Tickable` в граф.

## Инварианты и подводные камни

- **Регистрация — через `timerkit.Provide`.** Он ставит тег группы сам и
  требует `timerkit.Module`; конструктор неверного типа и граф без модуля валят
  сборку, а не молча теряют job.
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
