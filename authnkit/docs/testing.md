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

func (DeterministicHasher) Hash(plain string) (string, error)            // → "hash:" + plain
func (DeterministicHasher) Verify(plain, encoded string) (bool, error)   // encoded == "hash:"+plain
func StripHashPrefix(encoded string) string                              // убирает префикс "hash:"
```

`Hash` возвращает `"hash:" + plain` (детерминированно, без соли), `Verify`
сравнивает строкой, `StripHashPrefix` достаёт исходный plain из закодированной
строки — удобно для ассертов в тестах фикстур.

```go
var _ authnkit.Hasher = authntest.DeterministicHasher{} // удовлетворяет интерфейсу

h := authntest.DeterministicHasher{}
enc, _ := h.Hash("secret")             // "hash:secret"
ok, _ := h.Verify("secret", enc)       // true
plain := authntest.StripHashPrefix(enc) // "secret"
```

Хэш предсказуем и не секретен — только для тестов, никогда не в production-коде.

## JWT и TOTP: управляемые часы

`NewHS256JWT` и `NewTOTP` принимают `authnkit.WithClock(clockkit.Clock)`:
с `clockkit.NewFixed` / `clockkit.NewTickable` проверка `exp`, leeway и
TOTP-окон становится детерминированной без `time.Sleep`.

```go
clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
jwt, _ := authnkit.NewHS256JWT(cfg, authnkit.WithClock(clock))
tok, _ := jwt.Sign(claims, time.Minute)
clock.Advance(2 * time.Minute)
_, err := jwt.Parse(tok) // ErrTokenExpired
```

Секрет в тестовых `JWTConfig` — не короче 32 байт, иначе конструктор вернёт
`ErrConfig`. Для TOTP валидный код генерируй через
`totp.GenerateCode(secret, clock.Now())` (`github.com/pquerna/otp/totp`).

## RBAC

`Registry` экземплярный — в каждом тесте свой `NewRegistry()`, общего состояния
между тестами нет, параллелить безопасно.

## Прочее

Калибровка стойкости Argon2id под целевое железо — бенчмарки
`BenchmarkArgon2idHash` / `BenchmarkArgon2idVerify` в самом ките
(`go test -bench`). Fuzz-корпус `FuzzArgon2idVerify` / `FuzzJWTParse` гоняется
seed-ами в обычном `go test`; расширенный прогон — `go test -fuzz`.
