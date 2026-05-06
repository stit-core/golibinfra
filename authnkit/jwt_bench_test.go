package authnkit

import (
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

func BenchmarkJWTSign(b *testing.B) {
	h := NewHS256JWT(JWTConfig{Secret: "bench-secret-key"})
	claims := benchClaims()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Sign(claims, time.Minute); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJWTParse(b *testing.B) {
	h := NewHS256JWT(JWTConfig{Secret: "bench-secret-key"})
	tok, err := h.Sign(benchClaims(), time.Minute)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Parse(tok); err != nil {
			b.Fatal(err)
		}
	}
}
