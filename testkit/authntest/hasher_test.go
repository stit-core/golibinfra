package authntest_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/testkit/authntest"
)

var _ authnkit.Hasher = authntest.DeterministicHasher{}

func TestDeterministicHasher_HashThenVerify(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := authntest.DeterministicHasher{}
	enc, err := h.Hash(ctx, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if enc != "hash:secret" {
		t.Fatalf("Hash=%q", enc)
	}
	ok, err := h.Verify(ctx, "secret", enc)
	if err != nil || !ok {
		t.Fatalf("Verify(secret)=%v %v", ok, err)
	}
	ok, _ = h.Verify(ctx, "wrong", enc)
	if ok {
		t.Fatalf("Verify(wrong) must be false")
	}
	if h.NeedsRehash(enc) {
		t.Fatal("NeedsRehash must be false")
	}
	h.VerifyDummy(ctx)
}

// Предел длины пароля — как у боевого хэшера: тест домена видит тот же отказ.
func TestDeterministicHasher_PasswordTooLong(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := authntest.DeterministicHasher{}
	atLimit := strings.Repeat("p", authnkit.MaxPasswordLen)
	enc, err := h.Hash(ctx, atLimit)
	if err != nil {
		t.Fatalf("Hash of %d bytes: %v", authnkit.MaxPasswordLen, err)
	}
	if _, err := h.Hash(ctx, atLimit+"p"); !errors.Is(err, authnkit.ErrPasswordTooLong) {
		t.Fatalf("Hash of a too long password = %v, want ErrPasswordTooLong", err)
	}
	if ok, err := h.Verify(ctx, atLimit+"p", enc); ok || !errors.Is(err, authnkit.ErrPasswordTooLong) {
		t.Fatalf("Verify of a too long password = %v %v, want ErrPasswordTooLong", ok, err)
	}
}

// VerifyDummy — путь «пользователь не найден»: у детерминированного хешера
// выравнивать нечего, поэтому вызов ничего не считает и ничего не меняет.
func TestDeterministicHasher_VerifyDummy(t *testing.T) {
	t.Parallel()
	var h authnkit.Hasher = authntest.DeterministicHasher{}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	h.VerifyDummy(ctx)

	enc, err := h.Hash(t.Context(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	h.VerifyDummy(t.Context())
	h.VerifyDummy(t.Context())

	ok, err := h.Verify(t.Context(), "secret", enc)
	if err != nil || !ok {
		t.Fatalf("Verify после VerifyDummy = %v %v", ok, err)
	}
	if enc != authntest.HashPrefix+"secret" {
		t.Fatalf("Hash = %q: VerifyDummy не трогает состояние хешера", enc)
	}
}

func TestStripHashPrefix(t *testing.T) {
	t.Parallel()
	if got := authntest.StripHashPrefix(authntest.HashPrefix + "secret"); got != "secret" {
		t.Fatalf("StripHashPrefix = %q, want %q", got, "secret")
	}
	if got := authntest.StripHashPrefix("plain"); got != "plain" {
		t.Fatalf("StripHashPrefix on a value without the prefix = %q", got)
	}
}
