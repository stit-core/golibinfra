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

## Прочее

JWT, TOTP и RBAC отдельных тестовых дублей не требуют: `HS256JWT` дёшев и
детерминирован при фиксированном секрете; для TOTP в тестах генерируй валидный
код через `totp.GenerateCode(secret, time.Now())` (`github.com/pquerna/otp/totp`);
RBAC-реестр наполняется обычным `RegisterRolePermissions` — он process-global,
так что в тестах сохраняй/восстанавливай прежнее состояние, если параллелишь.

Калибровка стойкости Argon2id под целевое железо — бенчмарки
`BenchmarkArgon2idHash` / `BenchmarkArgon2idVerify` в самом ките
(`go test -bench`).
