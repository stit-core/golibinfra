package authnkit

import (
	"errors"
	"testing"
)

func fuzzHasher() *Argon2idHasher {
	return NewArgon2idHasher(Argon2idParams{
		Time:    1,
		Memory:  8 * 1024,
		Threads: 1,
		KeyLen:  16,
		SaltLen: 8,
	})
}

func FuzzArgon2idVerify(f *testing.F) {
	h := fuzzHasher()
	f.Add("")
	f.Add("$argon2id$v=19$m=65536,t=3,p=4$invalid$invalid")
	f.Add("not-a-hash")
	f.Add("$argon2id$v=19$m=65536,t=3,p=4$AAAA$BBBB")
	f.Add(phcString("m=8192,t=0,p=1", 16, 32))
	f.Add(phcString("m=8192,t=1,p=0", 16, 32))
	f.Add(phcString("m=8192,t=1,p=1", 16, 0))
	f.Add(phcString("m=4294967295,t=1,p=1", 16, 32))
	f.Add(phcString("m=8192,t=1,p=1", 0, 32))
	f.Add(phcString("m=8192,t=1,p=1", 16, 16))

	f.Fuzz(func(t *testing.T, encoded string) {
		ok, err := h.Verify("any", encoded)
		if err != nil && !errors.Is(err, ErrInvalidHash) {
			t.Fatalf("unexpected error type: %v", err)
		}
		if err != nil && ok {
			t.Fatal("ok with error")
		}
	})
}
