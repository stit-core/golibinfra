package authnkit

import "testing"

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

	f.Fuzz(func(t *testing.T, encoded string) {
		_, _ = h.Verify("any", encoded)

		good, err := h.Hash("foo")
		if err != nil {
			t.Fatalf("Hash: %v", err)
		}
		ok, err := h.Verify("foo", good)
		if err != nil {
			t.Fatalf("Verify foo: %v", err)
		}
		if !ok {
			t.Fatal("expected verify true for matching plain")
		}
		ok, err = h.Verify("bar", good)
		if err != nil {
			t.Fatalf("Verify bar: %v", err)
		}
		if ok {
			t.Fatal("expected verify false for non-matching plain")
		}
	})
}
