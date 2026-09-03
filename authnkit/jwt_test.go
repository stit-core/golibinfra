package authnkit

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

const testSecret = "test-secret-key-of-at-least-32-bytes!!"

func newTestHS256(t *testing.T, opts ...Option) *HS256JWT {
	t.Helper()
	h, err := NewHS256JWT(JWTConfig{Secret: testSecret}, opts...)
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	return h
}

func TestNewHS256JWT_Config(t *testing.T) {
	cases := map[string]JWTConfig{
		"short secret":     {Secret: "short"},
		"empty secret":     {},
		"negative leeway":  {Secret: testSecret, Leeway: -time.Second},
		"negative access":  {Secret: testSecret, AccessTTL: -time.Second},
		"negative refresh": {Secret: testSecret, RefreshTTL: -time.Second},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewHS256JWT(cfg); !errors.Is(err, ErrConfig) {
				t.Fatalf("expected ErrConfig, got %v", err)
			}
		})
	}
}

func TestHS256JWT_RoundTrip(t *testing.T) {
	h := newTestHS256(t)
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
	if _, ok := parsed["exp"].(float64); !ok {
		t.Errorf("exp: got %T, want float64", parsed["exp"])
	}
	if _, ok := parsed["iat"].(float64); !ok {
		t.Errorf("iat: got %T, want float64", parsed["iat"])
	}
}

func TestHS256JWT_SignAccessRefresh_UseConfigTTL(t *testing.T) {
	clock := clockkit.NewFixed(time.Unix(1_700_000_000, 0))
	h, err := NewHS256JWT(JWTConfig{Secret: testSecret, AccessTTL: 5 * time.Minute, RefreshTTL: 48 * time.Hour}, WithClock(clock))
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	expOf := func(tok string) int64 {
		t.Helper()
		claims, err := h.Parse(tok)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		return int64(claims["exp"].(float64))
	}

	access, _ := h.SignAccess(map[string]any{"sub": "u"})
	refresh, _ := h.SignRefresh(map[string]any{"sub": "u"})
	if got := expOf(access); got != clock.Now().Add(5*time.Minute).Unix() {
		t.Fatalf("access exp=%d", got)
	}
	if got := expOf(refresh); got != clock.Now().Add(48*time.Hour).Unix() {
		t.Fatalf("refresh exp=%d", got)
	}

	// Нулевые TTL в конфиге → дефолты кита, а не мгновенно протухшие токены.
	def, _ := NewHS256JWT(JWTConfig{Secret: testSecret}, WithClock(clock))
	access, _ = def.SignAccess(map[string]any{"sub": "u"})
	if got := expOf(access); got != clock.Now().Add(DefaultAccessTTL).Unix() {
		t.Fatalf("default access exp=%d", got)
	}
}

func TestHS256JWT_ExpiredToken(t *testing.T) {
	h := newTestHS256(t)
	// За пределами leeway (60s по умолчанию).
	tok, err := h.Sign(map[string]any{"sub": "x"}, -2*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, err = h.Parse(tok)
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestHS256JWT_Leeway(t *testing.T) {
	clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	signer := newTestHS256(t, WithClock(clock))
	tok, err := signer.Sign(map[string]any{"sub": "x"}, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	clock.Advance(90 * time.Second) // 30s после exp

	lenient, _ := NewHS256JWT(JWTConfig{Secret: testSecret, Leeway: time.Minute}, WithClock(clock))
	if _, err := lenient.Parse(tok); err != nil {
		t.Fatalf("within leeway: %v", err)
	}
	strict, _ := NewHS256JWT(JWTConfig{Secret: testSecret, Leeway: time.Second}, WithClock(clock))
	if _, err := strict.Parse(tok); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("beyond leeway: expected ErrTokenExpired, got %v", err)
	}
}

func TestHS256JWT_WrongSignature(t *testing.T) {
	signer := newTestHS256(t)
	tok, err := signer.Sign(map[string]any{"sub": "x"}, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	other, err := NewHS256JWT(JWTConfig{Secret: "other-secret-key-of-at-least-32-bytes!"})
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	_, err = other.Parse(tok)
	if !errors.Is(err, ErrTokenSignature) {
		t.Errorf("expected ErrTokenSignature, got %v", err)
	}
}

func TestHS256JWT_MalformedToken(t *testing.T) {
	h := newTestHS256(t)
	cases := []string{"", "not.a.token", "only-one-part", "a.b.c"}
	for _, tc := range cases {
		_, err := h.Parse(tc)
		if !errors.Is(err, ErrTokenMalformed) {
			t.Errorf("input %q: expected ErrTokenMalformed, got %v", tc, err)
		}
	}
}

func TestHS256JWT_RejectsAlgNone(t *testing.T) {
	h := newTestHS256(t)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x","exp":9999999999}`))
	for _, tok := range []string{header + "." + payload + ".", header + "." + payload} {
		if _, err := h.Parse(tok); !errors.Is(err, ErrTokenMalformed) {
			t.Errorf("alg none accepted: %v", err)
		}
	}
}

func TestHS256JWT_RejectsOtherHMACAlg(t *testing.T) {
	h := newTestHS256(t)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS512, Key: bytes.Repeat([]byte("k"), 64)}, nil)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	tok, err := jwt.Signed(signer).Claims(map[string]any{"sub": "x", "exp": time.Now().Add(time.Hour).Unix()}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := h.Parse(tok); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("HS512 accepted: %v", err)
	}
}

func TestHS256JWT_RequiresExp(t *testing.T) {
	h := newTestHS256(t)
	tok, err := jwt.Signed(h.signer).Claims(map[string]any{"sub": "x"}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := h.Parse(tok); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("token without exp accepted: %v", err)
	}
}

func TestHS256JWT_NotBeforeInFuture(t *testing.T) {
	h := newTestHS256(t)
	tok, err := jwt.Signed(h.signer).Claims(map[string]any{
		"sub": "x",
		"exp": time.Now().Add(time.Hour).Unix(),
		"nbf": time.Now().Add(time.Hour).Unix(),
	}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := h.Parse(tok); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("token with future nbf accepted: %v", err)
	}
}
