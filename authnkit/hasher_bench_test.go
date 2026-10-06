package authnkit

import (
	"context"
	"testing"
)

func benchHasher(b *testing.B) *Argon2idHasher {
	b.Helper()
	h, err := NewArgon2idHasher(Argon2idConfig{
		Time:    3,
		Memory:  64 * 1024,
		Threads: 4,
		KeyLen:  32,
		SaltLen: 16,
	})
	if err != nil {
		b.Fatal(err)
	}
	return h
}

func BenchmarkArgon2idHash(b *testing.B) {
	ctx := context.Background()
	h := benchHasher(b)
	b.ResetTimer()
	for b.Loop() {
		if _, err := h.Hash(ctx, "longenough"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkArgon2idVerify(b *testing.B) {
	ctx := context.Background()
	h := benchHasher(b)
	encoded, err := h.Hash(ctx, "longenough")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		ok, err := h.Verify(ctx, "longenough", encoded)
		if err != nil {
			b.Fatal(err)
		}
		if !ok {
			b.Fatal("verify false")
		}
	}
}
