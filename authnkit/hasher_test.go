package authnkit

import (
	"testing"
)

func defaultHasher() *Argon2idHasher {
	return NewArgon2idHasher(Argon2idParams{
		Time:    1,
		Memory:  64 * 1024,
		Threads: 4,
		KeyLen:  32,
		SaltLen: 16,
	})
}

func TestHashVerifyRoundtrip(t *testing.T) {
	h := defaultHasher()

	encoded, err := h.Hash("abc")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	ok, err := h.Verify("abc", encoded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatal("expected Verify to return true")
	}
}

func TestHashSaltIsRandom(t *testing.T) {
	h := defaultHasher()

	e1, err := h.Hash("abc")
	if err != nil {
		t.Fatalf("Hash 1: %v", err)
	}
	e2, err := h.Hash("abc")
	if err != nil {
		t.Fatalf("Hash 2: %v", err)
	}
	if e1 == e2 {
		t.Fatal("expected different encoded strings due to random salt")
	}
}

func TestVerifyWrongEncoded(t *testing.T) {
	h := defaultHasher()
	ok, err := h.Verify("abc", "$argon2id$v=19$m=65536,t=3,p=4$wrong$wrong")
	if err == nil {
		t.Fatal("expected error for wrong encoded")
	}
	if ok {
		t.Fatal("expected false for wrong encoded")
	}
}

func TestVerifyGarbageString(t *testing.T) {
	h := defaultHasher()
	ok, err := h.Verify("abc", "notahash")
	if err == nil {
		t.Fatal("expected error for garbage string")
	}
	if ok {
		t.Fatal("expected false for garbage string")
	}
}
