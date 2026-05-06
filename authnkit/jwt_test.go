package authnkit

import (
	"errors"
	"testing"
	"time"
)

func newTestHS256() *HS256JWT {
	return NewHS256JWT(JWTConfig{Secret: "test-secret-key"})
}

func TestHS256JWT_RoundTrip(t *testing.T) {
	h := newTestHS256()
	claims := map[string]any{"sub": "user-123", "role": "admin"}

	tok, err := h.Sign(claims, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if tok == "" {
		t.Fatal("expected non-empty token")
	}

	parsed, err := h.Parse(tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed["sub"] != "user-123" {
		t.Errorf("sub: got %v, want user-123", parsed["sub"])
	}
	if parsed["role"] != "admin" {
		t.Errorf("role: got %v, want admin", parsed["role"])
	}
}

func TestHS256JWT_ExpiredToken(t *testing.T) {
	h := newTestHS256()
	tok, err := h.Sign(map[string]any{"sub": "x"}, -time.Second)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, err = h.Parse(tok)
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestHS256JWT_WrongSignature(t *testing.T) {
	signer := newTestHS256()
	tok, err := signer.Sign(map[string]any{"sub": "x"}, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	other := NewHS256JWT(JWTConfig{Secret: "other-secret"})
	_, err = other.Parse(tok)
	if !errors.Is(err, ErrTokenSignature) {
		t.Errorf("expected ErrTokenSignature, got %v", err)
	}
}

func TestHS256JWT_MalformedToken(t *testing.T) {
	h := newTestHS256()
	cases := []string{"", "not.a.token", "only-one-part"}
	for _, tc := range cases {
		_, err := h.Parse(tc)
		if !errors.Is(err, ErrTokenMalformed) {
			t.Errorf("input %q: expected ErrTokenMalformed, got %v", tc, err)
		}
	}
}
