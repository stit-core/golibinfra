package authnkit

import "testing"

func benchHasher() *Argon2idHasher {
	return NewArgon2idHasher(Argon2idParams{
		Time:    3,
		Memory:  64 * 1024,
		Threads: 4,
		KeyLen:  32,
		SaltLen: 16,
	})
}

func BenchmarkArgon2idHash(b *testing.B) {
	h := benchHasher()
	b.ResetTimer()
	for b.Loop() {
		if _, err := h.Hash("longenough"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkArgon2idVerify(b *testing.B) {
	h := benchHasher()
	encoded, err := h.Hash("longenough")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		ok, err := h.Verify("longenough", encoded)
		if err != nil {
			b.Fatal(err)
		}
		if !ok {
			b.Fatal("verify false")
		}
	}
}
