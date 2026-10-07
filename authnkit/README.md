# authnkit

Криптографически-корректные кирпичики для аутентификации: `Hasher` поверх
Argon2id (пароль — до `MaxPasswordLen`, 1024 байта), JWT sign/verify на HS256 с типом токена, `iss`/`aud` из конфига и ротацией
секрета (`HS256JWT`, go-jose; `JWT_ISSUER`, `JWT_AUDIENCE`, `JWT_PREVIOUS_SECRET`),
TOTP-генератор по RFC 6238 (`TOTPGenerator`), одноразовые непрозрачные токены
(`NewToken`, `HashToken`), in-process RBAC-реестр (`Registry`) и сессии
refresh-токенов `authnkit/session` (ротация, отзыв сессии при повторе токена,
абсолютный срок, отзыв всех сессий пользователя; хранилище — `session/pgstore`).
Login-flow, MFA enrollment и хранение секретов живут на стороне потребителя. Observability-by-default: эмитит исход верификаций
JWT/TOTP и время хэширования/проверки пароля.

## Импорт

```go
import "git.stit.tech/stit-core/golibinfra/authnkit"
```

Версия и `go get` — см. [корневой README § Использование в проекте](../README.md).

## Быстрый старт

```go
package main

import (
	"context"
	"errors"
	"fmt"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

func main() {
	ctx := context.Background()

	// 1. Пароль: хэш → проверка → перехэш при смене параметров.
	hasher, err := authnkit.NewArgon2idHasher(authnkit.Argon2idConfig{
		Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
	})
	if err != nil {
		panic(err) // ErrConfig: параметры вне окна
	}
	// Пароль длиннее authnkit.MaxPasswordLen — ErrPasswordTooLong: DTO берёт тот же предел.
	encoded, _ := hasher.Hash(ctx, "s3cr3t")       // $argon2id$v=19$m=...$salt$hash
	ok, _ := hasher.Verify(ctx, "s3cr3t", encoded) // true
	if ok && hasher.NeedsRehash(encoded) {
		encoded, _ = hasher.Hash(ctx, "s3cr3t")
	}

	// 2. JWT: секрет ≥ 32 байт; тип токена проверяется при разборе.
	jwt, err := authnkit.NewHS256JWT(authnkit.JWTConfig{Secret: "change-me-to-a-32-byte-or-longer-secret"})
	if err != nil {
		panic(err) // ErrConfig: короткий секрет / отрицательные TTL
	}
	token, _ := jwt.SignAccess(ctx, map[string]any{"sub": "user-123", "role": "admin"})
	claims, err := jwt.ParseAccess(ctx, token)
	switch {
	case err == nil:
		fmt.Println("sub:", claims["sub"])
	case errors.Is(err, authnkit.ErrTokenExpired):
		// токен протух — отправить на refresh
	case errors.Is(err, authnkit.ErrWrongTokenType):
		// в access-контуре предъявили токен другой схемы (typ не access)
	}

	// 3. Одноразовый токен (ссылка сброса, подтверждение email): в письмо — токен,
	// в базу — только хеш; поиск — по хешу.
	resetToken := authnkit.NewToken()           // 64 hex = 256 бит, logkit маскирует
	resetHash := authnkit.HashToken(resetToken) // SHA-256 в hex — колонка с индексом
	_ = resetHash

	// 4. RBAC: реестр ролей (старт приложения) → проверка права.
	roles := authnkit.NewRegistry()
	roles.Register("admin", "articles:read", "articles:write")
	_ = roles.Has("admin", authnkit.Permission("articles:write")) // true

	_ = encoded
}
```

Сигнатуры API — в [docs/usage.md](docs/usage.md). Подключение через fx — там же.

## Документация

- [docs/architecture.md](docs/architecture.md) — устройство примитивов (hashing / JWT / TOTP / токены / RBAC) и сессий refresh, границы кита, проектные решения (WHY).
- [docs/configuration.md](docs/configuration.md) — полный справочник ENV: `Argon2idConfig`, `JWTConfig`, `TOTPConfig`, `session.Config`, окна параметров и их `Validate`.
- [docs/observability.md](docs/observability.md) — метрики и labels (единый источник правды).
- [docs/usage.md](docs/usage.md) — рецепты: fx Module и прямой конструктор, хэш пароля, sign/verify JWT, одноразовые токены, сессии refresh, TOTP с защитой от replay, проверка прав.
- [docs/testing.md](docs/testing.md) — `testkit/authntest`: `DeterministicHasher`; `WithClock` для JWT/TOTP; сессии: фейк и контракт `testkit/sessiontest`, Postgres с управляемыми часами.
