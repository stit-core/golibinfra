package authntest_test

import (
	"testing"

	"git.stit.tech/stit/golibinfra/authnkit"
	"git.stit.tech/stit/golibinfra/testkit/authntest"
)

var _ authnkit.Hasher = authntest.DeterministicHasher{}

func TestDeterministicHasher_HashThenVerify(t *testing.T) {
	t.Parallel()
	h := authntest.DeterministicHasher{}
	enc, err := h.Hash("secret")
	if err != nil {
		t.Fatal(err)
	}
	if enc != "hash:secret" {
		t.Fatalf("Hash=%q", enc)
	}
	ok, err := h.Verify("secret", enc)
	if err != nil || !ok {
		t.Fatalf("Verify(secret)=%v %v", ok, err)
	}
	ok, _ = h.Verify("wrong", enc)
	if ok {
		t.Fatalf("Verify(wrong) must be false")
	}
}
