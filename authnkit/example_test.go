package authnkit_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

// Example — программа README § Быстрый старт: импорты и тело равны блоку README байт в байт
// (TestReadme_QuickStart), вывод сверяет Output.
func Example() {
	ctx := context.Background()

	// 1. Пароль: хэш → проверка → перехэш при смене параметров.
	hasher, err := authnkit.NewArgon2idHasher(authnkit.Argon2idConfig{
		Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
	})
	if err != nil {
		panic(err) // ErrConfig: параметры вне окна
	}
	// Пароль длиннее authnkit.MaxPasswordLen (байты) — ErrPasswordTooLong: ошибка ввода, ответ 422.
	encoded, _ := hasher.Hash(ctx, "s3cr3t")       // $argon2id$v=19$m=...$salt$hash
	ok, _ := hasher.Verify(ctx, "s3cr3t", encoded) // true
	if ok && hasher.NeedsRehash(encoded) {
		encoded, _ = hasher.Hash(ctx, "s3cr3t")
	}

	// 2. JWT: секрет ≥ 32 байт и срок access-токена; тип токена проверяется при разборе.
	jwt, err := authnkit.NewHS256JWT(authnkit.JWTConfig{
		Secret:    "change-me-to-a-32-byte-or-longer-secret",
		AccessTTL: 15 * time.Minute,
	})
	if err != nil {
		panic(err) // ErrConfig: короткий секрет, AccessTTL не больше нуля
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
	// Output: sub: user-123
}
