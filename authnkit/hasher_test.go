package authnkit

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func defaultHasher() *Argon2idHasher {
	return NewArgon2idHasher(Argon2idParams{
		Time:    1,
		Memory:  8 * 1024,
		Threads: 1,
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
	ok, err = h.Verify("abd", encoded)
	if err != nil || ok {
		t.Fatalf("wrong password: ok=%v err=%v", ok, err)
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

func TestHash_InvalidParams(t *testing.T) {
	cases := map[string]Argon2idParams{
		"zero":         {},
		"t=0":          {Time: 0, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		"p=0":          {Time: 1, Memory: 8 * 1024, Threads: 0, KeyLen: 32, SaltLen: 16},
		"m too small":  {Time: 1, Memory: 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		"m too large":  {Time: 1, Memory: 2 * 1024 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		"short key":    {Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 8, SaltLen: 16},
		"short salt":   {Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 4},
		"too long key": {Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 256, SaltLen: 16},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewArgon2idHasher(p).Hash("x"); !errors.Is(err, ErrConfig) {
				t.Fatalf("expected ErrConfig, got %v", err)
			}
		})
	}
}

func phcString(params string, saltLen, hashLen int) string {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, saltLen))
	hash := base64.RawStdEncoding.EncodeToString(make([]byte, hashLen))
	return "$argon2id$v=19$" + params + "$" + salt + "$" + hash
}

// Параметры PHC-строки приходят из хранилища: вне окна → ErrInvalidHash, не
// паника в argon2 и не аллокация терабайтов.
func TestVerify_RejectsHostileParams(t *testing.T) {
	h := defaultHasher()
	cases := map[string]string{
		"t=0":                   phcString("m=8192,t=0,p=1", 16, 32),
		"p=0":                   phcString("m=8192,t=1,p=0", 16, 32),
		"empty hash":            phcString("m=8192,t=1,p=1", 16, 0),
		"short hash":            phcString("m=8192,t=1,p=1", 16, 8),
		"huge hash":             phcString("m=8192,t=1,p=1", 16, 129),
		"huge m":                phcString("m=4294967295,t=1,p=1", 16, 32),
		"m overflow":            phcString("m=99999999999,t=1,p=1", 16, 32),
		"m below window":        phcString("m=64,t=1,p=1", 16, 32),
		"negative t":            phcString("m=8192,t=-1,p=1", 16, 32),
		"p overflow":            phcString("m=8192,t=1,p=300", 16, 32),
		"short salt":            phcString("m=8192,t=1,p=1", 4, 32),
		"long salt":             phcString("m=8192,t=1,p=1", 65, 32),
		"wrong version":         strings.Replace(phcString("m=8192,t=1,p=1", 16, 32), "v=19", "v=16", 1),
		"missing param":         phcString("m=8192,t=1", 16, 32),
		"trailing garbage in p": phcString("m=8192,t=1,p=1x", 16, 32),
		"not argon2id":          strings.Replace(phcString("m=8192,t=1,p=1", 16, 32), "argon2id", "argon2i", 1),
		"bad base64":            "$argon2id$v=19$m=8192,t=1,p=1$***$***",
		"garbage":               "notahash",
		"empty":                 "",
		"leading garbage":       "x" + phcString("m=8192,t=1,p=1", 16, 32),
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := h.Verify("abc", encoded)
			if !errors.Is(err, ErrInvalidHash) {
				t.Fatalf("expected ErrInvalidHash, got %v", err)
			}
			if ok {
				t.Fatal("expected false")
			}
		})
	}
}

func TestNeedsRehash(t *testing.T) {
	h := defaultHasher()
	encoded, err := h.Hash("abc")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if h.NeedsRehash(encoded) {
		t.Fatal("own hash must not need rehash")
	}

	stronger := NewArgon2idHasher(Argon2idParams{Time: 2, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16})
	if !stronger.NeedsRehash(encoded) {
		t.Fatal("hash with older params must need rehash")
	}
	// Старый хэш при этом проверяется на своих параметрах.
	ok, err := stronger.Verify("abc", encoded)
	if err != nil || !ok {
		t.Fatalf("Verify with older params: ok=%v err=%v", ok, err)
	}
	if !h.NeedsRehash("garbage") {
		t.Fatal("unreadable hash must need rehash")
	}
}
