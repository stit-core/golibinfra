# bruteforcekit

Guard против brute-force на аутентификационные эндпойнты (login, OTP, reset). Кит
ведёт счётчик неудачных попыток по паре `(subject, ip)` — где `subject` это
логин / email / `phone:+…`, а `ip` адрес клиента — и при достижении порога
выставляет блокировку на заданное окно. Сервис спрашивает guard перед проверкой
пароля (`Check`), сообщает ему исход (`RecordFailure` / `RecordSuccess`) и
периодически чистит протухшие строки (`Cleanup`).

Core-абстракция (`Guard`) отделена от persistence: интерфейс живёт в корневом
пакете, реализация на Postgres — в подпакете `pgstore`. Это даёт подменить
хранилище в тестах или под другой backend, не трогая вызывающий код.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/bruteforcekit"
import "git.stit.tech/stit-core/golibinfra/bruteforcekit/pgstore"
```

Версия модуля — см. [`../README.md`](../README.md) (§ Использование в проекте).

## Быстрый старт

### fx-проводка

`pgstore.Module` отдаёт `*pgstore.Store` как `bruteforcekit.Guard` и регистрирует
миграцию кита в группе `pgkit.MigrateModule`. Ему нужны `*pgkit.DB` (из
`pgkit.Module`) и `bruteforcekit.Config` — конфиг кит **не** провайдит сам, его
подключает приложение (обычно `fx.Provide(bruteforcekit.NewConfig)`).

```go
fx.New(
    pgkit.Module,
    pgkit.MigrateModule, // применяет pgstore.Migrations

    fx.Provide(bruteforcekit.NewConfig), // BRUTEFORCE_* из ENV
    pgstore.Module,                      // *pgstore.Store как bruteforcekit.Guard

    fx.Invoke(func(g bruteforcekit.Guard) {
        // g уходит в auth-сервис
    }),
)
```

У корневого пакета `bruteforcekit` собственного `Module` нет — единственный
fx-вход это `pgstore.Module`.

### Прямой вызов

```go
cfg, err := bruteforcekit.NewConfig()
if err != nil { /* ... */ }

guard := pgstore.NewStore(db, cfg) // *pgkit.DB, bruteforcekit.Config
```

### Поток вызовов на login

`Check` до проверки пароля; исход — `RecordFailure` / `RecordSuccess`.

```go
func (s *AuthService) Login(ctx context.Context, email, ip, password string) error {
    lock, err := s.guard.Check(ctx, email, ip)
    if err != nil {
        return err
    }
    if lock != nil {
        return fmt.Errorf("locked until %s", lock.Until) // отдать 429 / 423
    }

    if !s.checkPassword(ctx, email, password) {
        if err := s.guard.RecordFailure(ctx, email, ip); err != nil {
            return err
        }
        return ErrInvalidCredentials
    }

    return s.guard.RecordSuccess(ctx, email, ip) // успех сбрасывает счётчик
}
```

`Cleanup` удаляет разблокированные и протухшие строки — его обычно вешают на
периодический job (например через `timerkit`); операция идемпотентна.

```go
if err := guard.Cleanup(ctx); err != nil {
    log.Error(ctx, "bruteforce cleanup", "err", err)
}
```

## Конфигурация

`bruteforcekit.Config` (`config.go`) — **part of public API**: имена полей и
envconfig-теги менять = breaking change, идёт мажорным апгрейдом.

| Env | Поле | Тип | Default | Назначение |
|---|---|---|---|---|
| `BRUTEFORCE_MAX_FAILURES` | `MaxFailures` | `int` (`>= 1`) | `5` | Порог: при достижении этого числа неудач по паре `(subject, ip)` строка блокируется; `1` — блокировка с первой неудачи |
| `BRUTEFORCE_LOCK_DURATION` | `LockDuration` | `time.Duration` (`> 0`) | `15m` | Длительность блокировки от момента достижения порога (`locked_until = NOW() + LockDuration`), с точностью до долей секунды |
| `BRUTEFORCE_CLEANUP_RETENTION` | `CleanupRetention` | `time.Duration` (`>= 0`) | `24h` | Возраст строки по `last_attempt`, после которого `Cleanup` удаляет её (только если она не под активной блокировкой) |

`NewConfig()` читает теги через `envconfig.Process` и валидирует значения
(`Config.Validate`): порог меньше 1, неположительная длительность блокировки или
отрицательный retention — ошибка.

## Observability

Кит **ничего не эмитит** — ни метрик, ни спанов. Запросы к Postgres идут через
`*pgkit.DB`, поэтому per-query спаны (`otelpgx`) и метрики пула (`db_pool_*`)
приходят от `pgkit`, а не от `bruteforcekit`. Видимость самих lock-событий —
ответственность вызывающего сервиса (например, `bizmetric`-счётчик на возврат
ненулевого `LockState`).

## API

### `LockState`

```go
type LockState struct {
    Until time.Time
}
```

Возвращается из `Check` указателем: `nil` означает «не заблокировано», ненулевой
`*LockState` — блокировка активна до `Until`.

### `Guard`

Core-интерфейс (`guard.go`); реализуется `pgstore.Store`.

```go
type Guard interface {
    Check(ctx context.Context, subject, ip string) (*LockState, error)
    RecordFailure(ctx context.Context, subject, ip string) error
    RecordSuccess(ctx context.Context, subject, ip string) error
    Cleanup(ctx context.Context) error
}
```

- `Check` — `nil, nil`, если строки нет или `locked_until` уже в прошлом по часам
  базы (`locked_until > NOW()` в SQL, часы процесса не участвуют); иначе
  `*LockState{Until}` при активной блокировке.
- `RecordFailure` — upsert по `(subject, ip)`: инкремент `failed_count`, обновление
  `last_attempt`; при `failed_count >= MaxFailures` (включая первую вставку при
  `MaxFailures = 1`) выставляет `locked_until`, иначе сбрасывает его в `NULL`.
- `RecordSuccess` — удаляет строку `(subject, ip)`, обнуляя историю попыток.
- `Cleanup` — удаляет строки, которые разблокированы (`locked_until IS NULL` или
  уже в прошлом) **и** старше `CleanupRetention` по `last_attempt`. Идемпотентна.

### `pgstore.Store`

```go
func pgstore.NewStore(db *pgkit.DB, cfg bruteforcekit.Config) *Store
```

Реализация `Guard` на одной таблице `bruteforce_attempts` с составным первичным
ключом `(subject, ip)`. `pgstore.Module` провайдит `*Store` как
`bruteforcekit.Guard` (через `fx.Annotate` / `fx.As`) и регистрирует
`pgstore.Migrations` в группе `pgkit.migrations`.

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
CREATE INDEX bruteforce_attempts_locked_idx
    ON bruteforce_attempts (locked_until) WHERE locked_until IS NOT NULL;
```

## Тестирование

Интеграционные тесты (`pgstore/store_test.go`) поднимают реальный Postgres через
`testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))`. В режиме
`-short` они скипаются (`testing.Short()`), поэтому юнит-прогон их не запускает;
полный прогон требует Docker.

Покрытие: блокировка после `MaxFailures` (`Until` в ожидаемом окне), блокировка с
первой неудачи при `MaxFailures = 1`, `Check` по часам базы (истёкший
`locked_until` — не блокировка), сброс счётчика после `RecordSuccess`, выживание
заблокированной строки при `Cleanup` и удаление протухшей, `Config.Validate`.

Для юнит-тестов вызывающего кода без БД core-интерфейс `bruteforcekit.Guard`
подменяется фейками из [`testkit/bruteforcetest`](../testkit/bruteforcetest/) —
`pgstore` для этого не нужен.

## Инварианты и подводные камни

- **Счётчик ведётся на пару `(subject, ip)`, не отдельно per-subject и per-IP.**
  Ключ строки — составной `(subject, ip)`, поэтому неудачи с одного логина с разных
  IP считаются как разные строки и блокируются независимо; аналогично один IP против
  разных логинов. Если нужны независимые лимиты «N на subject» и «M на IP» — это
  отдельные политики поверх двух вызовов с разными ключами, а не текущая модель.
- **`Check` отделён от учёта.** `Check` только читает блокировку и не инкрементит
  счётчик; рост — исключительно через `RecordFailure`. Не забыть вызвать
  `RecordFailure` на каждую неудачу проверки пароля.
- **`RecordSuccess` обнуляет историю удалением строки.** Успешный вход стирает
  накопленные неудачи по `(subject, ip)` целиком — частичного декремента нет.
- **Lock переоткрывается при дальнейших неудачах.** Пока строка живёт, каждый
  `RecordFailure` при `failed_count >= MaxFailures` пересчитывает
  `locked_until = NOW() + LockDuration`, то есть продолжающиеся попытки продлевают
  блокировку. Если же `failed_count` ещё ниже порога, `RecordFailure` сбрасывает
  `locked_until` в `NULL`.
- **Unlock — пассивный, по времени.** Явного «разблокировать» нет: `Check`
  возвращает `nil`, как только `locked_until` оказывается в прошлом. Сама строка
  при этом остаётся до `Cleanup` (или до `RecordSuccess`).
- **Скользящего окна нет.** Подсчёт неудач идёт монотонным `failed_count` в
  строке — накопленные неудачи не «истекают» сами, они уходят только через
  `RecordSuccess` либо `Cleanup`. Эффективное обнуление history для
  незаблокированных строк даёт `Cleanup` по `CleanupRetention`.
- **Всё время — часы базы.** `locked_until`, `last_attempt` и их сравнение в
  `Check`/`Cleanup` считаются через `NOW()` Postgres; рассинхрон часов между
  инстансами сервиса на блокировки не влияет.
- **`Cleanup` не трогает активные блокировки.** Удаляются только разблокированные
  строки старше `CleanupRetention`; заблокированная строка переживает `Cleanup`
  независимо от возраста.
- **core+pgstore split.** Интерфейс и `Config` — в `bruteforcekit`, SQL и fx —
  в `pgstore`. Корневой пакет `Module` не экспортирует; единственный fx-вход —
  `pgstore.Module`, а `bruteforcekit.Config` приложение провайдит явно (kit его
  сам в граф не кладёт).
