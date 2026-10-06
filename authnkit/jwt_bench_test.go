package authnkit

import (
	"context"
	"testing"
	"time"
)

func benchClaims() map[string]any {
	return map[string]any{
		"sub":    "user-123",
		"role":   "admin",
		"tenant": "t1",
		"scope":  "read write",
		"jti":    "abc-def-ghi",
	}
}

func benchJWT(b *testing.B) *HS256JWT {
	b.Helper()
	h, err := NewHS256JWT(JWTConfig{AccessTTL: 15 * time.Minute, Secret: "bench-secret-key-of-at-least-32-bytes!"})
	if err != nil {
		b.Fatal(err)
	}
	return h
}

func BenchmarkJWTSign(b *testing.B) {
	ctx := context.Background()
	h := benchJWT(b)
	claims := benchClaims()
	b.ResetTimer()
	for b.Loop() {
		if _, err := h.Sign(ctx, claims, time.Minute); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJWTParse(b *testing.B) {
	ctx := context.Background()
	h := benchJWT(b)
	tok, err := h.Sign(ctx, benchClaims(), time.Minute)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := h.Parse(ctx, tok); err != nil {
			b.Fatal(err)
		}
	}
}
