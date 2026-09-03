package authnkit

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

func TestTOTP_GenerateAndVerify(t *testing.T) {
	gen := NewTOTP(TOTPConfig{Issuer: "TestIssuer", Skew: 1})

	secret, url, err := gen.Generate("user@example.com")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if secret == "" {
		t.Error("expected non-empty secret")
	}
	if url == "" {
		t.Error("expected non-empty url")
	}

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}

	if !gen.Verify(secret, code) {
		t.Error("expected Verify to return true for valid code")
	}
}

func TestTOTP_InvalidCode(t *testing.T) {
	gen := NewTOTP(TOTPConfig{Issuer: "TestIssuer", Skew: 1})

	secret, _, err := gen.Generate("user@example.com")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if gen.Verify(secret, "000000") {
		t.Error("expected Verify to return false for invalid code (statistically)")
	}
	if gen.Verify("not-base32-secret!!", "123456") {
		t.Error("expected Verify to return false for unreadable secret")
	}
}

func TestTOTP_Skew(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := clockkit.NewFixed(now)
	secret, _, err := NewTOTP(TOTPConfig{Issuer: "TestIssuer"}).Generate("u")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	previous, err := totp.GenerateCode(secret, now.Add(-30*time.Second))
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}

	if !NewTOTP(TOTPConfig{Issuer: "TestIssuer", Skew: 1}, WithClock(clock)).Verify(secret, previous) {
		t.Error("skew=1 must accept the previous window")
	}
	if NewTOTP(TOTPConfig{Issuer: "TestIssuer", Skew: 0}, WithClock(clock)).Verify(secret, previous) {
		t.Error("skew=0 must reject the previous window")
	}
}
