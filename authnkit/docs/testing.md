# authnkit — тестирование

Тестовый хелпер живёт в сабпакете `testkit/authntest` (вне самого кита,
однородно с другими `testkit/*test` сабпакетами).

```go
import "git.stit.tech/stit-core/golibinfra/testkit/authntest"
```

## DeterministicHasher

Реализация `authnkit.Hasher` без криптографии: подставляется в доменные тесты
вместо `Argon2idHasher`, чтобы они не платили десятки миллисекунд на каждый хэш
и были детерминированными.

```go
type DeterministicHasher struct {
	HashErr error // сбой хэширования: Hash отдаёт его вместо хеша
	Busy    bool  // очередь полна: Hash, Verify и VerifyDummy отдают authnkit.ErrBusy
}

const HashPrefix = "hash:"

func (*DeterministicHasher) Hash(ctx context.Context, plain string) (string, error)          // → "hash:" + plain
func (*DeterministicHasher) Verify(ctx context.Context, plain, encoded string) (bool, error) // encoded == "hash:"+plain
func (*DeterministicHasher) NeedsRehash(encoded string) bool                                 // true — строка без "hash:"
func (*DeterministicHasher) VerifyDummy(ctx context.Context) error                           // счёт вызова; nil, при Busy — ErrBusy
func (*DeterministicHasher) VerifyCalls() int                                                // сколько раз звали Verify
func (*DeterministicHasher) VerifyDummyCalls() int                                           // сколько раз звали VerifyDummy
func StripHashPrefix(encoded string) string                                                  // убирает префикс "hash:"
```

`Hash` возвращает `"hash:" + plain` (детерминированно, без соли), `Verify`
сравнивает строкой, `StripHashPrefix` достаёт исходный plain из закодированной
строки — удобно для ассертов в тестах фикстур. Отказы — как у боевого хэшера:
пароль длиннее `authnkit.MaxPasswordLen` `Hash` и `Verify` отвергают
`authnkit.ErrPasswordTooLong`, строку без `HashPrefix` `Verify` —
`authnkit.ErrInvalidHash`, а `NeedsRehash` считает её устаревшей.

```go
h := &authntest.DeterministicHasher{}   // нулевое значение готово; Hasher — указатель
enc, _ := h.Hash(ctx, "secret")         // "hash:secret"
ok, _ := h.Verify(ctx, "secret", enc)   // true
plain := authntest.StripHashPrefix(enc) // "secret"
```

Сбой хэширования, полная очередь (`Busy` — `authnkit.ErrBusy` в том же порядке
исходов, что у боевого хэшера: длинный пароль у `Hash` — ошибка ввода до очереди,
нечитаемый хеш у `Verify` — до очереди) и число проверок пароля — поведение самого
фейка, своя обёртка в тесте не нужна:

```go
uc := NewUsecase(&authntest.DeterministicHasher{HashErr: errHashing}) // Hash отдаёт errHashing

h := &authntest.DeterministicHasher{}
_ = NewUsecase(h).Login(ctx, "nobody@example.com", "pw")
// вход по неизвестному адресу — ровно одна холостая проверка: время ответа не выдаёт учётную запись
if h.VerifyCalls() != 0 || h.VerifyDummyCalls() != 1 { … }

busy := &authntest.DeterministicHasher{Busy: true}
guard := &bruteforcetest.Recorder{}
err := NewUsecase(busy, guard).Login(ctx, "nobody@example.com", "pw")
// перегрузка на пути без учётной записи — тот же отказ, что с ней: errors.Is(err, authnkit.ErrBusy);
// попытка guard'а, учтённая до проверки, возвращена: len(guard.Refunds()) == 1
```

Хэш предсказуем и не секретен — только для тестов. Предохранитель на случай
ошибки проводки fx: вне `go test` (`testing.Testing() == false`) любой вызов
хэшера паникует, а не кладёт пароли в базу открытым текстом.

## JWT и TOTP: управляемые часы

`NewHS256JWT`, `NewTOTP` и `NewMemoryUsedCodeStore` принимают
`authnkit.WithClock(clockkit.Clock)`: с `clockkit.NewFixed` /
`clockkit.NewTickable` проверка `exp`, leeway, TOTP-окон и истечения записей
об использованных кодах становится детерминированной без `time.Sleep`.

```go
clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
jwt, _ := authnkit.NewHS256JWT(cfg, authnkit.WithClock(clock))
tok, _ := jwt.SignAccess(ctx, claims)
clock.Advance(30 * time.Minute)
_, err := jwt.ParseAccess(ctx, tok) // ErrTokenExpired
```

Секрет в тестовых `JWTConfig` — не короче 32 байт, иначе конструктор вернёт
`ErrConfig`; то же с `TOTPConfig.Issuer` (непустой, без `:` и `/`) и
`Argon2idConfig` (окно). Для TOTP валидный код генерируй через
`totp.GenerateCode(secret, clock.Now())` (`github.com/pquerna/otp/totp`).

## Сессии refresh

Юнит-тест сценария (вход, обновление, блокировка пользователя) берёт фейк
`sessiontest.New(cfg, clock)` — in-memory `session.Store` с семантикой pgstore;
`Active(subject)` показывает, сколько сессий открыто после `RevokeAll`.
`sessiontest.RunContract(t, newStore)` — общий контракт `session.Store`: его проходят и
фейк, и `pgstore.Store`, так что поведение в юнит-тесте не расходится с продом (в том числе
владелец из `Revoke` — только у вызова, закрывшего сессию, и окно повтора: повтор в окне —
новый преемник, прежний погашен; вне окна, после использования преемника, у закрытой или
истёкшей сессии — `ErrReused`). Окно у кейсов набора — минута (`Config.RetryWindow`), у
кейсов строгого режима — ноль: `newStore` берёт `Config` кейса.
Откат транзакции фейк не видит — `RevokeAll` в откатанной транзакции
проверяется на pgstore. Параллельные `Rotate` одним токеном в окне фейк и pgstore проводят
одинаково — по очереди, каждый повтор гасит преемника предыдущего, сессия жива, и из выданных
токенов живым остаётся один: набор предъявляет их снова — ротаций не больше одной, и хранилище, у
которого повтор проверяет преемника и гасит его не атомарно, на этом краснеет.

Сбой потребителя после `Rotate` (ответ клиенту не ушёл) и повтор тем же токеном:

```go
lost, _ := sessions.Rotate(ctx, tok.Value)        // ответ потерян
clock.Advance(2 * time.Second)                     // меньше cfg.RetryWindow
retried, err := sessions.Rotate(ctx, tok.Value)    // nil: новый преемник той же сессии
_, err = sessions.Rotate(ctx, lost.Value)          // session.ErrReused: преемник погашен, сессия отозвана
```

`pgstore.Store` проверяется на Postgres из `testkit.StartPostgres` с миграциями
кита; сроки — часами `pgstore.WithClock(clockkit.NewTickable(...))`, без `sleep`:

```go
pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
clock := clockkit.NewTickable(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
sessions, _ := pgstore.NewStore(cfg, pg.DB(t), pgstore.WithClock(clock))
tok, _ := sessions.Issue(ctx, "user-1")
clock.Advance(cfg.IdleTTL)
_, err := sessions.Rotate(ctx, tok.Value) // session.ErrExpired
```

Лесенка миграций — `testkit.MigrationLadder(t, testkit.WithMigrations(pgstore.Migrations))`.

## RBAC

`Registry` экземплярный — в каждом тесте свой `NewRegistry()`, общего состояния
между тестами нет, параллелить безопасно.

## Прочее

Калибровка стойкости Argon2id под целевое железо — бенчмарки
`BenchmarkArgon2idHash` / `BenchmarkArgon2idVerify` в самом ките
(`go test -bench`). Fuzz-корпус `FuzzArgon2idVerify` / `FuzzJWTParse` гоняется
seed-ами в обычном `go test`; расширенный прогон — `go test -fuzz`.
