# bruteforcekit

Guard против brute-force на аутентификационные эндпойнты (login, OTP, reset). Кит
ведёт счётчик попыток по паре `(subject, ip)` — где `subject` это логин / email /
`phone:+…`, а `ip` адрес клиента — и после `MaxFailures` попыток без успеха
выставляет блокировку на заданное окно. Сервис учитывает попытку до проверки
пароля (`Attempt`) и сообщает об успехе (`RecordSuccess`), а чистка протухших
строк живёт отдельно (`Cleaner.Cleanup`) и работает job'ом.

Core-абстракции (`Guard`, `Cleaner`) отделены от persistence: интерфейсы живут в
корневом пакете, реализация на Postgres — в подпакете `pgstore`. Это даёт
подменить хранилище в тестах или под другой backend, не трогая вызывающий код.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/bruteforcekit"
import "git.stit.tech/stit-core/golibinfra/bruteforcekit/pgstore"
```

Версия модуля — см. [`../README.md`](../README.md) (§ Использование в проекте).

## Быстрый старт

### fx-проводка

`pgstore.Module` отдаёт `*pgstore.Store` как `bruteforcekit.Guard` и
`bruteforcekit.Cleaner`, регистрирует миграции кита в группе
`pgkit.MigrateModule` и cleanup-job в группе `timerkit.Group`. Ему нужны `*pgkit.DB`
(из `pgkit.Module`), `bruteforcekit.Config` (конфиг кит **не** провайдит сам:
`fx.Provide(bruteforcekit.NewConfig)`) и `timerkit.Module` — без него граф не
собирается (`missing type: timerkit.scheduler`): cleanup-job, которую никто не
запускает, оставила бы таблицу расти.

```go
import (
    "context"
    "errors"
    "fmt"

    "go.uber.org/fx"

    "git.stit.tech/stit-core/golibinfra/bruteforcekit"
    "git.stit.tech/stit-core/golibinfra/bruteforcekit/pgstore"
    "git.stit.tech/stit-core/golibinfra/httpkit"
    "git.stit.tech/stit-core/golibinfra/logkit"
    "git.stit.tech/stit-core/golibinfra/pgkit"
    "git.stit.tech/stit-core/golibinfra/timerkit"
)

func app() *fx.App {
    return fx.New(
        fx.Provide(
            logkit.NewConfig,
            logkit.New,
            pgkit.NewPoolConfig,          // PG_*
            pgkit.NewMigrateRunnerConfig, // PG_MIGRATIONS_PATH, PG_MIGRATE_TIMEOUT
            bruteforcekit.NewConfig,      // BRUTEFORCE_*
        ),
        pgkit.Module,
        pgkit.MigrateModule, // применяет pgstore.Migrations
        timerkit.Module,     // запускает cleanup-job кита
        pgstore.Module,      // *pgstore.Store как Guard и Cleaner

        fx.Provide(NewAuthService),
        fx.Invoke(func(*AuthService) {}),
    )
}
```

### Прямой вызов

`NewStore` валидирует `Config` и возвращает ошибку: нулевой конфиг —
`bruteforcekit.ErrConfig`, а не guard, который молча никогда не блокирует.

```go
cfg, err := bruteforcekit.NewConfig()
if err != nil { /* ... */ }

guard, err := pgstore.NewStore(db, cfg) // *pgkit.DB, bruteforcekit.Config
if err != nil { /* ... */ }
```

### Поток вызовов на login

`Attempt` — до проверки пароля и вне транзакции: он атомарно учитывает попытку
своим коммитом и отвечает, можно ли проверять секрет. Успех — `RecordSuccess`
(можно и в транзакции логина); неудача ничего не требует — попытка уже учтена.

```go
type AuthService struct {
    guard bruteforcekit.Guard
    users UserRepo
}

func NewAuthService(guard bruteforcekit.Guard, users UserRepo) *AuthService {
    return &AuthService{guard: guard, users: users}
}

func (s *AuthService) Login(ctx context.Context, email, ip, password string) error {
    lock, err := s.guard.Attempt(ctx, email, ip)
    if err != nil {
        return err
    }
    if lock != nil {
        return fmt.Errorf("locked until %s", lock.Until) // отдать 429 / 423
    }

    if !s.users.CheckPassword(ctx, email, password) {
        return ErrInvalidCredentials // попытка уже учтена
    }

    return s.guard.RecordSuccess(ctx, email, ip) // успех сбрасывает окно
}
```

`ip` берётся из транспорта — `httpkit.ClientIPFrom(ctx)`:

```go
ip := httpkit.ClientIPFrom(ctx)
if err := auth.Login(ctx, email, ip, password); err != nil {
    if errors.Is(err, bruteforcekit.ErrInvalidKey) || errors.Is(err, bruteforcekit.ErrInTx) {
        /* баг проводки, 500 */
    }
}
```

## Требования к `ip`

Сила guard'а равна доверию к `ip`: если сервис берёт адрес из подделываемого
заголовка, атакующий подставляет новый IP на каждую попытку, каждая попытка
попадает в новую строку и блокировка не наступает никогда.

- **Источник — только доверенный адрес.** `httpkit.ClientIPFrom(ctx)` отдаёт
  peer-адрес соединения, а запись `X-Forwarded-For` — лишь при
  `HTTP_TRUST_PROXY_HOPS=N` конфига `httpkit`, то есть когда сервис стоит **за** N
  доверенными прокси: берётся N-я справа запись, которую дописали они. Публичный
  сервис без прокси оставляет `0`.
- **Канонизация — на стороне кита.** `Config.NormalizeKey` приводит адрес к
  канонической форме (`netip`): `::1` и `0:0:0:0:0:0:0:1`, `2001:DB8::1` и
  `2001:db8::1`, `::ffff:203.0.113.7` и `203.0.113.7` — один бакет счётчика;
  зона (`fe80::1%eth0`) отбрасывается. Приводить адрес заранее не нужно.
- **IPv6 агрегируется до `/64`.** Провайдер выдаёт клиенту целую подсеть,
  поэтому по умолчанию (`BRUTEFORCE_IPV6_PREFIX=64`) все адреса одной `/64`
  попадают в общий бакет, и перебор адресов подсети не обходит блокировку;
  `128` (`FullIPv6Prefix`) ведёт счётчик на полный адрес. На IPv4 параметр не
  влияет. Ту же свёртку `/64` делает ключ лимитера `httpkit`.
- **Пустой или неразбираемый `ip` — ошибка.** `Attempt` и `RecordSuccess`
  возвращают `bruteforcekit.ErrInvalidKey`, не выполняя запрос:
  «ip забыли передать» — баг проводки, а не бакет `""` на все адреса subject'а.
  Адрес с портом (`203.0.113.7:443`) и CIDR (`203.0.113.0/24`) тоже отвергаются.

## Требования к `subject`

- Непустой и не длиннее `bruteforcekit.MaxSubjectLen` (255 байт) — иначе
  `ErrInvalidKey` до запроса. Предел равен ограничению btree-индекса Postgres:
  без него длинный subject ронял бы `Attempt` ошибкой базы, а сервис
  отдавал бы 500 вместо «неверные учётные данные».
- Длину и форму значения (`email ≤ 254`, `phone:+…`) валидирует caller в DTO
  (например `maxLength` у huma), кит только отсекает заведомо непригодные ключи.
- Кит нормализует subject: обрезает пробелы по краям и приводит к нижнему
  регистру (`strings.ToLower`), поэтому `User@Example.com ` и `user@example.com`
  — один бакет, и варианты регистра не дают новых попыток. Пустой после обрезки
  subject — `ErrInvalidKey`. Остальную канонизацию формы (`phone:+…`) делает
  caller.

## Конфигурация

`bruteforcekit.Config` (`config.go`) — **part of public API**: имена полей и
envconfig-теги менять = breaking change; выпускается minor-релизом, помечается
`!` в Conventional Commit и строкой миграции в аннотации тега (см. [корневой
README § Версионирование](../README.md)). `NewConfig()`
читает ENV ридером китов (корневой README § Стабильность API) и валидирует значения
(`Config.Validate`); любое нарушение — ошибка, обёрнутая `bruteforcekit.ErrConfig`. Нулевой `Config`
отвергается.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `BRUTEFORCE_MAX_FAILURES` | `MaxFailures` | `int` (`>= 1`) | `5` | Порог: при достижении этого числа неудач в текущем окне пара `(subject, ip)` блокируется; `1` — блокировка с первой неудачи |
| `BRUTEFORCE_LOCK_DURATION` | `LockDuration` | `time.Duration` (`> 0`) | `15m` | Длительность блокировки от момента достижения порога (`locked_until = NOW() + LockDuration`) |
| `BRUTEFORCE_CLEANUP_RETENTION` | `CleanupRetention` | `time.Duration` (`>= 0`) | `24h` | Возраст строки по `last_attempt`, после которого `Cleanup` удаляет её (только если она не под активной блокировкой) |
| `BRUTEFORCE_CLEANUP_INTERVAL` | `CleanupInterval` | `time.Duration` (`> 0`) | `5m` | Период cleanup-job'а, который `pgstore.Module` кладёт в группу `timerkit.Group`; он же дедлайн одного прогона |
| `BRUTEFORCE_IPV6_PREFIX` | `IPv6Prefix` | `int` (`[1, 128]`) | `64` | Длина префикса, до которой агрегируются IPv6-адреса; `64` — бакет на `/64`-подсеть, `128` — без агрегации |

## Observability

`pgstore.Store` эмитит через глобальные OTel-провайдеры (`otel.Meter`,
scope `git.stit.tech/stit-core/golibinfra/bruteforcekit`); без зарегистрированного
`MeterProvider` инструменты — no-op.

| Метрика | Тип | Labels | Смысл |
|---|---|---|---|
| `bruteforce_checks_total` | counter | `result=allowed\|locked` | Результат `Attempt`: попытка пропущена к проверке секрета или упёрлась в блокировку |
| `bruteforce_locks_total` | counter | — | Переход пары в заблокированное состояние: блокировку выставила попытка, на которой счётчик окна достиг порога. Попытки под активной блокировкой счётчик не двигают — одно событие на окно |
| `bruteforce_cleanup_deleted_total` | counter | — | Строки, удалённые `Cleanup` (суммарно по батчам) |

Ошибки ключа (`ErrInvalidKey`) и базы в счётчики не попадают: их видно по
ошибкам вызывающего кода. Спанов кит не заводит — per-query спаны даёт `otelpgx`
из `pgkit`, метрики пула — `pg_pool_*`.

Всплеск `bruteforce_locks_total` — security-сигнал (распределённая атака или
поломанный клиент), рост `bruteforce_checks_total{result="locked"}` при плоском
`bruteforce_locks_total` — заблокированные пользователи, которые продолжают
стучаться.

## API

### `LockState`

```go
type LockState struct {
    Until       time.Time
    FailedCount int
}
```

Возвращается из `Attempt` указателем: `nil` — попытка разрешена, ненулевой
`*LockState` — пара заблокирована до `Until`. `FailedCount` — попытки текущего
окна без успешного входа на момент ответа.

### `Guard`

Core-интерфейс request-path (`guard.go`); реализуется `pgstore.Store`.

```go
type Guard interface {
    Attempt(ctx context.Context, subject, ip string) (*LockState, error)
    RecordSuccess(ctx context.Context, subject, ip string) error
}
```

- `Attempt` — upsert по `(subject, ip)` до проверки секрета. Пока активной
  блокировки нет, он инкрементит `failed_count` (или ставит `1`, если
  предыдущая блокировка истекла, — новое окно) и `last_attempt`; попытка, на
  которой счётчик достигает `MaxFailures`, выставляет `locked_until` и сама ещё
  проходит. Под активной блокировкой (`locked_until > NOW()` по часам базы)
  строка не пишется, и `Attempt` отдаёт `*LockState`. Upsert атомарен: из N
  одновременных попыток пары проходят не больше `MaxFailures`. Внутри
  транзакции вызывающего (`pgkit.InTx`) — `bruteforcekit.ErrInTx` без запроса:
  откат на неверном пароле снял бы учёт.
- `RecordSuccess` — удаляет строку `(subject, ip)`: история попыток и
  блокировка, выставленная последней попыткой окна, снимаются.

### `Cleaner`

```go
type Cleaner interface {
    Cleanup(ctx context.Context) error
}
```

Maintenance-путь, отдельный от `Guard`: его реализуют только хранилища, а фейки
и адаптеры вокруг `Guard` — нет. `Cleanup` удаляет строки, которые
разблокированы (`locked_until IS NULL` или уже в прошлом) **и** старше
`CleanupRetention` по `last_attempt`, батчами по 5000 строк до исчерпания.
Идемпотентен.

### `pgstore.Store`

```go
func pgstore.NewStore(db *pgkit.DB, cfg bruteforcekit.Config) (*Store, error)
```

Реализация `Guard` и `Cleaner` на одной таблице `bruteforce_attempts` с
составным первичным ключом `(subject, ip)`. Конструктор валидирует `cfg`
(`ErrConfig`). `pgstore.Module` провайдит `*Store` под обоими интерфейсами,
кладёт `pgstore.Migrations` в группу `pgkit.migrations` и `CleanupJob` — в
группу `timerkit.Group` (`"timerkit.jobs"`).

### `pgstore.CleanupJob`

```go
func pgstore.CleanupJob(s *Store, cfg bruteforcekit.Config) timerkit.Job
```

Job `bruteforcekit.cleanup` с интервалом и таймаутом `CleanupInterval`; первый
прогон — через интервал после старта, схема к этому моменту уже применена.
`Module` регистрирует его сам, вручную вызывать не нужно.

### Миграции

`pgstore.Migrations` — `pgkit.Source{Name: "bruteforcekit"}` (таблица версий
`schema_migrations_bruteforcekit`) с `migrations/*.sql` (embed).
`pgkit.MigrateModule` применяет его на старте автоматически; без fx —
`pgkit.MigrateUpAll(ctx, dsn, pgstore.Migrations, ...)`. Таблица:

```sql
CREATE TABLE bruteforce_attempts (
    subject        TEXT        NOT NULL,
    ip             TEXT        NOT NULL,
    failed_count   INT         NOT NULL DEFAULT 0,
    locked_until   TIMESTAMPTZ,
    last_attempt   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (subject, ip)
);
CREATE INDEX bruteforce_attempts_last_attempt_idx ON bruteforce_attempts (last_attempt);
```

`Attempt` и `RecordSuccess` ходят по первичному ключу; индекс по `last_attempt`
обслуживает выборку батча в `Cleanup`.

## Тестирование

Интеграционные тесты (`pgstore/*_test.go`) поднимают реальный Postgres через
`testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))`. Гейт
держит сам helper (`testkit.RequireDocker`): под `-short` тест пропускается,
собственного `testing.Short()` в тесте нет. Полный прогон требует Docker: без
него тест падает.

Покрытие: обратимость миграций (`testkit.MigrationLadder`: down `0002`
возвращает `bruteforce_attempts_locked_idx`), блокировка после `MaxFailures`
попыток и после единственной при `MaxFailures=1`, попытка под блокировкой строку
не пишет, независимость бакетов, блокировка по часам базы, сброс после
`RecordSuccess` (и на последней попытке окна), новое окно после истечения
блокировки, сохранение активной блокировки при поднятии порога и блокировка при
понижении, нормализация subject'а, канонизация `ip` и агрегация `/64`,
атомарность `Attempt` под конкурентными попытками
(`TestStore_ConcurrentAttempts`: из 50 проходят `MaxFailures`), `Attempt` в
транзакции (`ErrInTx`, строки нет) и откат успеха (счётчик остаётся), `Cleanup`
(свежие и заблокированные строки, батчи, индекс миграции `0002`), метрики, отказ
`NewStore`/`Module` на невалидном конфиге, `NewConfig` с `ENV` и `NormalizeKey`.

Для юнит-тестов вызывающего кода без БД `bruteforcekit.Guard` подменяется
фейками из [`testkit/bruteforcetest`](../testkit/bruteforcetest/): `Permissive`
(всегда пропускает), `Locked{Until}` (всегда заблокирован), `Recorder`
(запоминает пары `subject`/`ip` — `Attempts()`, `Successes()`), `Counting` (та
же политика в памяти: `N` попыток → lock, часы теста через `clockkit.Tickable`).

Контракт фейки держат наравне с `pgstore`: пустой или слишком длинный
`subject`, пустой либо неразбираемый `ip` — `ErrInvalidKey` из любого метода,
`Attempt` внутри транзакции (фейк `pgtest` помечает ctx) — `ErrInTx`, и в
историю `Recorder` такой вызов не попадает. Проводка, которая отдаёт
guard'у пустой `ip`, падает в юнит-тесте, а не на проде. Ключ, как pgstore,
сворачивает только `Counting` (subject в нижнем регистре, IPv6 по
`Config.IPv6Prefix`) — у остальных фейков конфига нет, и `Recorder` хранит пару
как передана.

## Инварианты и подводные камни

- **Счётчик ведётся на пару `(subject, ip)`, не отдельно per-subject и per-IP.**
  Ключ строки — составной `(subject, ip)`, поэтому неудачи с одного логина с разных
  IP считаются как разные строки и блокируются независимо; аналогично один IP против
  разных логинов. Если нужны независимые лимиты «N на subject» и «M на IP» — это
  отдельные политики поверх двух вызовов с разными ключами, а не текущая модель.
- **Попытка учитывается до проверки секрета, своим коммитом.** `Attempt` —
  единственный вход request-path, и неудача ничего не требует от сервиса. В
  транзакции вызывающего (`Transactor.WithTx`) `Attempt` отвечает `ErrInTx`:
  откат на неверном пароле снял бы учёт, и перебор шёл бы без предела.
  `RecordSuccess` в транзакции допустим — её откат оставляет счётчик
  (fail-closed); без успеха окно пары продолжает копиться.
- **Параллельные попытки не обходят порог.** `Attempt` — один upsert по строке
  пары: одновременные попытки сериализуются на ней, и к проверке секрета
  успевают пройти не больше `MaxFailures`, остальные получают `*LockState`.
  Лимит на параллельные запросы в целом — задача rate-limiter'а (`httpkit`), а
  не guard'а.
- **Окно закрывается блокировкой.** Попытка, на которой `failed_count` достигает
  `MaxFailures`, блокирует пару на `LockDuration`; попытки под активной
  блокировкой строку не трогают — ни счётчик, ни срок не растут. Как только
  блокировка истекла, следующая попытка открывает **новое** окно
  (`failed_count = 1`) — один раз попавший под lock пользователь не
  блокируется навсегда одной ошибкой в сутки.
- **Смена порога.** Изменённый `BRUTEFORCE_MAX_FAILURES` не снимает активные
  блокировки: строка, у которой `locked_until` в будущем, остаётся
  заблокированной до конца окна. После понижения порога попытка, чей счётчик
  его превысил, блокируется сразу.
- **`RecordSuccess` обнуляет историю удалением строки.** Успешный вход стирает
  накопленные неудачи по `(subject, ip)` целиком — частичного декремента нет.
- **Unlock — пассивный, по времени.** Явного «разблокировать» нет: `Attempt`
  снова пропускает, как только `locked_until` оказывается в прошлом. Сама строка
  при этом остаётся до `Cleanup` (или до `RecordSuccess`).
- **Скользящего окна нет.** Внутри окна неудачи не «истекают» сами: счётчик
  обнуляют `RecordSuccess`, истечение блокировки и `Cleanup` по
  `CleanupRetention`.
- **Cleanup-job обязателен.** Рост таблицы attacker-driven: строка заводится на
  каждую новую пару `(subject, ip)`, включая несуществующие subject'ы. Поэтому
  `pgstore.Module` без `timerkit.Module` в графе не собирается.
- **Всё время — часы базы.** `locked_until`, `last_attempt` и их сравнение в
  `Attempt`/`Cleanup` считаются через `NOW()` Postgres; рассинхрон часов между
  инстансами сервиса на блокировки не влияет. Внутри `pgkit.Transactor.WithTx`
  `NOW()` — момент начала транзакции.
- **`Cleanup` не трогает активные блокировки.** Удаляются только разблокированные
  строки старше `CleanupRetention`; заблокированная строка переживает `Cleanup`
  независимо от возраста.
- **core+pgstore split.** Интерфейсы и `Config` — в `bruteforcekit`, SQL и fx —
  в `pgstore`. Корневой пакет `Module` не экспортирует; единственный fx-вход —
  `pgstore.Module`, а `bruteforcekit.Config` приложение провайдит явно (kit его
  сам в граф не кладёт).
