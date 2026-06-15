# authnkit

Криптографически-корректные кирпичики для аутентификации: `Hasher` поверх
Argon2id, JWT sign/verify на HS256 (`HS256JWT`), TOTP-генератор по RFC 6238
(`TOTPGenerator`) и in-process RBAC-примитивы (`Permission`, `RolePermissions`,
`Has`). Кит даёт только примитивы — конкретные auth-flow'ы (login, refresh,
выдача/ротация токенов, MFA enrollment, хранение секретов) живут на стороне
потребителя. Observability-by-default: эмитит исход верификаций JWT/TOTP и время
хэширования/проверки пароля.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/authnkit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

```go
package main

import (
	"errors"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

func main() {
	// 1. Пароль: хэш → проверка.
	hasher := authnkit.NewArgon2idHasher(authnkit.Argon2idParams{
		Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
	})
	encoded, _ := hasher.Hash("s3cr3t")          // $argon2id$v=19$m=...$salt$hash
	ok, _ := hasher.Verify("s3cr3t", encoded)    // true

	// 2. JWT: подпись с TTL → разбор claims.
	jwt := authnkit.NewHS256JWT(authnkit.JWTConfig{Secret: "change-me"})
	token, _ := jwt.Sign(map[string]any{"sub": "user-123", "role": "admin"}, 15*time.Minute)
	claims, err := jwt.Parse(token)
	switch {
	case err == nil:
		fmt.Println("sub:", claims["sub"])
	case errors.Is(err, authnkit.ErrTokenExpired):
		// токен протух — отправить на refresh
	}

	// 3. RBAC: регистрация ролей (старт приложения) → проверка права.
	authnkit.RegisterRolePermissions("admin", "articles:read", "articles:write")
	_ = authnkit.Has("admin", authnkit.Permission("articles:write")) // true

	_ = ok
}
```

Сигнатуры API — в [docs/usage.md](docs/usage.md). Подключение через fx — там же.

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство четырёх примитивов (hashing / JWT / TOTP / RBAC), границы кита, проектные решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV: `Argon2idParams` + `JWTConfig`, тюнинг стойкости Argon2id.
- [docs/observability.md](docs/observability.md) — метрики и labels (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: fx Module и прямой конструктор, хэш пароля, sign/verify JWT, TOTP, проверка прав.
- [docs/testing.md](docs/testing.md) — `testkit/authntest`: `DeterministicHasher`.
