package authnkit

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

func newTestTOTP(t *testing.T, cfg TOTPConfig, opts ...TOTPOption) *TOTPGenerator {
	t.Helper()
	g, err := NewTOTP(cfg, opts...)
	if err != nil {
		t.Fatalf("NewTOTP: %v", err)
	}
	return g
}

func TestTOTPConfig_Validate(t *testing.T) {
	bad := map[string]TOTPConfig{
		"empty issuer":      {},
		"issuer with colon": {Issuer: "My:Service"},
		"issuer with slash": {Issuer: "My/Service"},
		"skew above window": {Issuer: "svc", Skew: MaxTOTPSkew + 1},
		"huge skew":         {Issuer: "svc", Skew: 4294967295},
	}
	for name, cfg := range bad {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); !errors.Is(err, ErrConfig) {
				t.Fatalf("Validate: expected ErrConfig, got %v", err)
			}
			if _, err := NewTOTP(cfg); !errors.Is(err, ErrConfig) {
				t.Fatalf("NewTOTP: expected ErrConfig, got %v", err)
			}
		})
	}
	if err := (TOTPConfig{Issuer: "My Service", Skew: MaxTOTPSkew}).Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

func TestTOTP_GenerateAndVerify(t *testing.T) {
	ctx := t.Context()
	gen := newTestTOTP(t, TOTPConfig{Issuer: "TestIssuer", Skew: 1})

	secret, otpauthURL, err := gen.Generate("user@example.com")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if secret == "" || otpauthURL == "" {
		t.Fatalf("secret=%q url=%q", secret, otpauthURL)
	}

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	ok, err := gen.Verify(ctx, secret, code)
	if err != nil || !ok {
		t.Fatalf("Verify(valid code): ok=%v err=%v", ok, err)
	}
}

// otpauth-URL читают authenticator-приложения: параметры в нём должны быть
// теми же, по которым считает Verify.
func TestTOTP_GenerateURL(t *testing.T) {
	gen := newTestTOTP(t, TOTPConfig{Issuer: "My Service", Skew: 1})
	secret, otpauthURL, err := gen.Generate("user@example.com")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	u, err := url.Parse(otpauthURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Scheme != "otpauth" || u.Host != "totp" {
		t.Fatalf("scheme=%q host=%q", u.Scheme, u.Host)
	}
	if u.Path != "/My Service:user@example.com" {
		t.Fatalf("label = %q", u.Path)
	}
	// Пробел в issuer экранируется, иначе URL не разобрать.
	if u.EscapedPath() != "/My%20Service:user@example.com" {
		t.Fatalf("escaped label = %q", u.EscapedPath())
	}
	q := u.Query()
	want := map[string]string{
		"issuer":    "My Service",
		"secret":    secret,
		"period":    "30",
		"digits":    "6",
		"algorithm": "SHA1",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
}

func TestTOTP_GenerateRejectsAmbiguousAccount(t *testing.T) {
	gen := newTestTOTP(t, TOTPConfig{Issuer: "TestIssuer"})
	for _, account := range []string{"", "us/er", "us:er"} {
		if _, _, err := gen.Generate(account); !errors.Is(err, ErrConfig) {
			t.Fatalf("account %q: expected ErrConfig, got %v", account, err)
		}
	}
}

func TestTOTP_InvalidCodeAndSecret(t *testing.T) {
	ctx := t.Context()
	gen := newTestTOTP(t, TOTPConfig{Issuer: "TestIssuer", Skew: 1})

	secret, _, err := gen.Generate("user@example.com")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	ok, err := gen.Verify(ctx, secret, "000000")
	if err != nil {
		t.Fatalf("wrong code must not be an error: %v", err)
	}
	if ok {
		t.Error("expected false for an invalid code (statistically)")
	}

	// Битый секрет из хранилища отличим от неверного кода.
	ok, err = gen.Verify(ctx, "not-base32-secret!!", "123456")
	if ok || err == nil {
		t.Fatalf("unreadable secret: ok=%v err=%v", ok, err)
	}
}

func TestTOTP_Skew(t *testing.T) {
	ctx := t.Context()
	now := time.Unix(1_700_000_000, 0)
	clock := clockkit.NewFixed(now)
	secret, _, err := newTestTOTP(t, TOTPConfig{Issuer: "TestIssuer"}).Generate("u")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	previous, err := totp.GenerateCode(secret, now.Add(-30*time.Second))
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}

	lenient := newTestTOTP(t, TOTPConfig{Issuer: "TestIssuer", Skew: 1}, WithClock(clock))
	if ok, err := lenient.Verify(ctx, secret, previous); err != nil || !ok {
		t.Errorf("skew=1 must accept the previous window: ok=%v err=%v", ok, err)
	}
	strict := newTestTOTP(t, TOTPConfig{Issuer: "TestIssuer", Skew: 0}, WithClock(clock))
	if ok, err := strict.Verify(ctx, secret, previous); err != nil || ok {
		t.Errorf("skew=0 must reject the previous window: ok=%v err=%v", ok, err)
	}
}

func TestTOTP_VerifyOnce(t *testing.T) {
	ctx := t.Context()
	now := time.Unix(1_700_000_000, 0)
	clock := clockkit.NewTickable(now)
	gen := newTestTOTP(t, TOTPConfig{Issuer: "TestIssuer", Skew: 1}, WithClock(clock))
	store := NewMemoryUsedCodeStore(WithClock(clock))

	secret, _, err := gen.Generate("u")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}

	if ok, err := gen.VerifyOnce(ctx, secret, code, store); err != nil || !ok {
		t.Fatalf("first use: ok=%v err=%v", ok, err)
	}
	// Тот же код в том же окне — replay.
	if ok, err := gen.VerifyOnce(ctx, secret, code, store); err != nil || ok {
		t.Fatalf("replay: ok=%v err=%v", ok, err)
	}
	// Другой секрет с тем же кодом не блокируется чужой записью.
	other, _, err := gen.Generate("u2")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	otherCode, err := totp.GenerateCode(other, now)
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	if ok, err := gen.VerifyOnce(ctx, other, otherCode, store); err != nil || !ok {
		t.Fatalf("second secret: ok=%v err=%v", ok, err)
	}

	// Неверный код до хранилища не доходит.
	if ok, err := gen.VerifyOnce(ctx, secret, "000000", store); err != nil || ok {
		t.Fatalf("invalid code: ok=%v err=%v", ok, err)
	}

	// Запись живёт своё окно ±Skew и после истечения снимается.
	clock.Advance(gen.codeLifetime() + time.Second)
	if ok, err := store.MarkUsed(ctx, "k", time.Minute); err != nil || !ok {
		t.Fatalf("expired entry must be swept: ok=%v err=%v", ok, err)
	}
}

func TestTOTP_VerifyOnce_StoreError(t *testing.T) {
	ctx := t.Context()
	now := time.Unix(1_700_000_000, 0)
	gen := newTestTOTP(t, TOTPConfig{Issuer: "TestIssuer"}, WithClock(clockkit.NewFixed(now)))
	secret, _, err := gen.Generate("u")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}

	ok, err := gen.VerifyOnce(ctx, secret, code, failingStore{})
	if ok || err == nil {
		t.Fatalf("store error must not pass the code: ok=%v err=%v", ok, err)
	}
	if !errors.Is(err, errStore) {
		t.Fatalf("store error must be wrapped: %v", err)
	}
}

var errStore = errors.New("store down")

type failingStore struct{}

func (failingStore) MarkUsed(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return false, errStore
}

var _ UsedCodeStore = (*MemoryUsedCodeStore)(nil)
