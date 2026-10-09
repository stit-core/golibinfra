package authnkit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

// Сниппет README § Быстрый старт работает как написан: литерал конфига без обязательного поля —
// ErrConfig уже у конструктора.
func TestReadme_QuickStart(t *testing.T) {
	ctx := context.Background()

	hasher, err := authnkit.NewArgon2idHasher(authnkit.Argon2idConfig{
		Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
	})
	if err != nil {
		t.Fatalf("NewArgon2idHasher: %v", err)
	}
	encoded, err := hasher.Hash(ctx, "s3cr3t")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if ok, err := hasher.Verify(ctx, "s3cr3t", encoded); err != nil || !ok || hasher.NeedsRehash(encoded) {
		t.Fatalf("Verify = %v, %v, NeedsRehash = %v; want true, nil, false", ok, err, hasher.NeedsRehash(encoded))
	}

	jwt, err := authnkit.NewHS256JWT(authnkit.JWTConfig{
		Secret:    "change-me-to-a-32-byte-or-longer-secret",
		AccessTTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	token, err := jwt.SignAccess(ctx, map[string]any{"sub": "user-123", "role": "admin"})
	if err != nil {
		t.Fatalf("SignAccess: %v", err)
	}
	if claims, err := jwt.ParseAccess(ctx, token); err != nil || claims["sub"] != "user-123" {
		t.Fatalf("ParseAccess = %v, %v; want sub user-123", claims, err)
	}

	resetToken := authnkit.NewToken()
	if len(resetToken) != 64 || len(authnkit.HashToken(resetToken)) != 64 {
		t.Fatalf("NewToken %q, HashToken %q; want 64 hex each", resetToken, authnkit.HashToken(resetToken))
	}

	roles := authnkit.NewRegistry()
	roles.Register("admin", "articles:read", "articles:write")
	if !roles.Has("admin", authnkit.Permission("articles:write")) {
		t.Fatal("Registry.Has(admin, articles:write) = false")
	}
}
