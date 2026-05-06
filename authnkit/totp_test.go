package authnkit

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestTOTP_GenerateAndVerify(t *testing.T) {
	gen := NewTOTP("TestIssuer")

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
	gen := NewTOTP("TestIssuer")

	secret, _, err := gen.Generate("user@example.com")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if gen.Verify(secret, "000000") {
		t.Error("expected Verify to return false for invalid code (statistically)")
	}
}
