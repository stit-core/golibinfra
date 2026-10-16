package authntest_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/testkit/authntest"
)

var _ authnkit.Hasher = (*authntest.DeterministicHasher)(nil)

func TestDeterministicHasher_HashThenVerify(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := &authntest.DeterministicHasher{}
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
	ok, err = h.Verify(ctx, "wrong", enc)
	if ok || err != nil {
		t.Fatalf("Verify(wrong) = %v %v, want false without an error", ok, err)
	}
	if h.NeedsRehash(enc) {
		t.Fatal("NeedsRehash of an own hash must be false")
	}
}

// Предел длины пароля — как у боевого хэшера: тест домена видит тот же отказ.
func TestDeterministicHasher_PasswordTooLong(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := &authntest.DeterministicHasher{}
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

// Регрессия KTST-05: строку не своего формата фейк разбирает как боевой хэшер — тест домена с
// битым хешем в фикстуре видит ErrInvalidHash, а не «пароль не подошёл».
func TestDeterministicHasher_ForeignHashLikeArgon2id(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	argon, err := authnkit.NewArgon2idHasher(authnkit.Argon2idConfig{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8})
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]authnkit.Hasher{"argon2id": argon, "fake": &authntest.DeterministicHasher{}} {
		for _, encoded := range []string{"garbage", ""} {
			if ok, err := h.Verify(ctx, "pw", encoded); ok || !errors.Is(err, authnkit.ErrInvalidHash) {
				t.Errorf("%s: Verify against %q = %v %v, want ErrInvalidHash", name, encoded, ok, err)
			}
			if !h.NeedsRehash(encoded) {
				t.Errorf("%s: NeedsRehash(%q) = false, want true: an unreadable hash is rehashed", name, encoded)
			}
		}
		long := strings.Repeat("p", authnkit.MaxPasswordLen+1)
		if _, err := h.Verify(ctx, long, "garbage"); !errors.Is(err, authnkit.ErrPasswordTooLong) {
			t.Errorf("%s: Verify of a too long password against garbage = %v, want ErrPasswordTooLong first", name, err)
		}
	}
}

// Сбой хэширования — поведение фейка: usecase, который теряет ошибку Hash, тест ловит без
// своей обёртки над хэшером.
func TestDeterministicHasher_HashErr(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	boom := errors.New("argon2: out of memory")
	h := &authntest.DeterministicHasher{HashErr: boom}
	if enc, err := h.Hash(ctx, "secret"); enc != "" || !errors.Is(err, boom) {
		t.Fatalf("Hash = %q %v, want HashErr and no hash", enc, err)
	}
	if _, err := h.Hash(ctx, strings.Repeat("p", authnkit.MaxPasswordLen+1)); !errors.Is(err, authnkit.ErrPasswordTooLong) {
		t.Fatalf("Hash of a too long password = %v, want ErrPasswordTooLong before HashErr", err)
	}
	if ok, err := h.Verify(ctx, "secret", authntest.HashPrefix+"secret"); !ok || err != nil {
		t.Fatalf("Verify = %v %v: HashErr must not break verification", ok, err)
	}
}

// Счёт проверок пароля: тест входа видит, что на каждом пути — известный адрес или нет — ушла
// ровно одна проверка, настоящая или холостая.
func TestDeterministicHasher_CountsVerifyAndVerifyDummy(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := &authntest.DeterministicHasher{}
	if h.VerifyCalls() != 0 || h.VerifyDummyCalls() != 0 {
		t.Fatalf("fresh hasher: verify=%d dummy=%d, want 0 0", h.VerifyCalls(), h.VerifyDummyCalls())
	}
	if _, err := h.Hash(ctx, "secret"); err != nil {
		t.Fatal(err)
	}
	_ = h.NeedsRehash("hash:secret")
	if h.VerifyCalls() != 0 || h.VerifyDummyCalls() != 0 {
		t.Fatalf("after Hash and NeedsRehash: verify=%d dummy=%d, want 0 0", h.VerifyCalls(), h.VerifyDummyCalls())
	}

	_, _ = h.Verify(ctx, "secret", "hash:secret")
	_, _ = h.Verify(ctx, "wrong", "hash:secret")
	_, _ = h.Verify(ctx, "secret", "garbage")
	_, _ = h.Verify(ctx, strings.Repeat("p", authnkit.MaxPasswordLen+1), "hash:secret")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, c := range []context.Context{cancelled, ctx} {
		if err := h.VerifyDummy(c); err != nil {
			t.Fatalf("VerifyDummy = %v, want nil: the fake has no queue to wait in", err)
		}
	}
	if h.VerifyCalls() != 4 || h.VerifyDummyCalls() != 2 {
		t.Fatalf("verify=%d dummy=%d, want every call counted: 4 and 2", h.VerifyCalls(), h.VerifyDummyCalls())
	}
}

// Busy — полная очередь боевого хэшера: тест потребителя получает ErrBusy на каждом пути пароля, в том же порядке
// исходов, что у боевого (длинный пароль у Hash — ошибка ввода до очереди, у Verify — ErrBusy холостого вычисления,
// нечитаемый хеш — до очереди), и вызовы проверки по-прежнему считаются.
func TestDeterministicHasher_Busy(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := &authntest.DeterministicHasher{Busy: true}
	tooLong := strings.Repeat("p", authnkit.MaxPasswordLen+1)
	var retry interface{ RetryAfter() time.Duration }

	if _, err := h.Hash(ctx, "secret"); !errors.Is(err, authnkit.ErrBusy) || !errors.As(err, &retry) {
		t.Fatalf("Hash = %v, want ErrBusy with a retry hint", err)
	}
	if _, err := h.Hash(ctx, tooLong); !errors.Is(err, authnkit.ErrPasswordTooLong) {
		t.Fatalf("Hash of a too long password = %v, want ErrPasswordTooLong before the queue", err)
	}
	for _, c := range []struct {
		name, plain, encoded string
		want                 error
	}{
		{"right password", "secret", "hash:secret", authnkit.ErrBusy},
		{"too long password", tooLong, "hash:secret", authnkit.ErrBusy},
		{"unreadable hash", "secret", "garbage", authnkit.ErrInvalidHash},
	} {
		if ok, err := h.Verify(ctx, c.plain, c.encoded); ok || !errors.Is(err, c.want) {
			t.Errorf("Verify, %s = %v %v, want %v", c.name, ok, err, c.want)
		}
	}
	if err := h.VerifyDummy(ctx); !errors.Is(err, authnkit.ErrBusy) {
		t.Fatalf("VerifyDummy = %v, want ErrBusy", err)
	}
	if h.VerifyCalls() != 3 || h.VerifyDummyCalls() != 1 {
		t.Fatalf("verify=%d dummy=%d, want refused calls counted too: 3 and 1", h.VerifyCalls(), h.VerifyDummyCalls())
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
