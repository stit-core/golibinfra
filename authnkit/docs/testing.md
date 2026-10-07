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
type DeterministicHasher struct{}

const HashPrefix = "hash:"

func (DeterministicHasher) Hash(ctx context.Context, plain string) (string, error)          // → "hash:" + plain
func (DeterministicHasher) Verify(ctx context.Context, plain, encoded string) (bool, error) // encoded == "hash:"+plain
func (DeterministicHasher) NeedsRehash(encoded string) bool                                 // всегда false
func (DeterministicHasher) VerifyDummy(ctx context.Context)                                 // no-op
func StripHashPrefix(encoded string) string                                                 // убирает префикс "hash:"
```

`Hash` возвращает `"hash:" + plain` (детерминированно, без соли), `Verify`
сравнивает строкой, `StripHashPrefix` достаёт исходный plain из закодированной
строки — удобно для ассертов в тестах фикстур. Пароль длиннее
`authnkit.MaxPasswordLen` оба метода отвергают `authnkit.ErrPasswordTooLong`, как
боевой хэшер.

```go
var _ authnkit.Hasher = authntest.DeterministicHasher{} // удовлетворяет интерфейсу

h := authntest.DeterministicHasher{}
enc, _ := h.Hash(ctx, "secret")         // "hash:secret"
ok, _ := h.Verify(ctx, "secret", enc)   // true
plain := authntest.StripHashPrefix(enc) // "secret"
```

Хэш предсказуем и не секретен — только для тестов. Предохранитель на случай
ошибки проводки fx: вне `go test` (`testing.Testing() == false`) `Hash` и
`Verify` паникуют, а не кладут пароли в базу открытым текстом.

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
владелец из `Revoke` — только у вызова, закрывшего сессию).
Откат транзакции фейк не видит — `RevokeAll` в откатанной транзакции
проверяется на pgstore.

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
