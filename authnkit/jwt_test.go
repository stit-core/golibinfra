package authnkit

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

const testSecret = "test-secret-key-of-at-least-32-bytes!!"

func newTestHS256(t *testing.T, opts ...JWTOption) *HS256JWT {
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
			if err := cfg.Validate(); !errors.Is(err, ErrConfig) {
				t.Fatalf("Validate: expected ErrConfig, got %v", err)
			}
		})
	}
	if err := (JWTConfig{Secret: testSecret}).Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

func TestHS256JWT_RoundTrip(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	claims := map[string]any{"sub": "user-123", "role": "admin"}

	tok, err := h.Sign(ctx, claims, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if tok == "" {
		t.Fatal("expected non-empty token")
	}

	parsed, err := h.Parse(ctx, tok)
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
	// Sign не навязывает тип: типизированы только SignAccess/SignRefresh.
	if _, ok := parsed[ClaimTokenType]; ok {
		t.Errorf("Sign must not set %q", ClaimTokenType)
	}
}

func TestHS256JWT_SignRejectsNonPositiveTTL(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := h.Sign(ctx, map[string]any{"sub": "x"}, ttl); !errors.Is(err, ErrConfig) {
			t.Fatalf("ttl %s: expected ErrConfig, got %v", ttl, err)
		}
	}
}

func TestHS256JWT_SignAccessRefresh_UseConfigTTL(t *testing.T) {
	ctx := t.Context()
	clock := clockkit.NewFixed(time.Unix(1_700_000_000, 0))
	h, err := NewHS256JWT(JWTConfig{Secret: testSecret, AccessTTL: 5 * time.Minute, RefreshTTL: 48 * time.Hour}, WithClock(clock))
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	expOf := func(tok string) int64 {
		t.Helper()
		claims, err := h.Parse(ctx, tok)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		return int64(claims["exp"].(float64))
	}

	access, err := h.SignAccess(ctx, map[string]any{"sub": "u"})
	if err != nil {
		t.Fatalf("SignAccess: %v", err)
	}
	refresh, err := h.SignRefresh(ctx, map[string]any{"sub": "u"})
	if err != nil {
		t.Fatalf("SignRefresh: %v", err)
	}
	if got := expOf(access); got != clock.Now().Add(5*time.Minute).Unix() {
		t.Fatalf("access exp=%d", got)
	}
	if got := expOf(refresh); got != clock.Now().Add(48*time.Hour).Unix() {
		t.Fatalf("refresh exp=%d", got)
	}

	// Нулевые TTL в конфиге → дефолты кита, а не мгновенно протухшие токены.
	def, err := NewHS256JWT(JWTConfig{Secret: testSecret}, WithClock(clock))
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	access, err = def.SignAccess(ctx, map[string]any{"sub": "u"})
	if err != nil {
		t.Fatalf("SignAccess: %v", err)
	}
	if got := expOf(access); got != clock.Now().Add(DefaultAccessTTL).Unix() {
		t.Fatalf("default access exp=%d", got)
	}
}

// Refresh-токен в access-cookie не должен проходить как access.
func TestHS256JWT_TokenTypeIsChecked(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)

	access, err := h.SignAccess(ctx, map[string]any{"sub": "u"})
	if err != nil {
		t.Fatalf("SignAccess: %v", err)
	}
	refresh, err := h.SignRefresh(ctx, map[string]any{"sub": "u"})
	if err != nil {
		t.Fatalf("SignRefresh: %v", err)
	}

	claims, err := h.ParseAccess(ctx, access)
	if err != nil {
		t.Fatalf("ParseAccess(access): %v", err)
	}
	if claims[ClaimTokenType] != TokenTypeAccess {
		t.Fatalf("access %s=%v", ClaimTokenType, claims[ClaimTokenType])
	}
	if _, err := h.ParseRefresh(ctx, refresh); err != nil {
		t.Fatalf("ParseRefresh(refresh): %v", err)
	}

	if _, err := h.ParseAccess(ctx, refresh); !errors.Is(err, ErrWrongTokenType) {
		t.Fatalf("refresh accepted as access: %v", err)
	}
	if _, err := h.ParseRefresh(ctx, access); !errors.Is(err, ErrWrongTokenType) {
		t.Fatalf("access accepted as refresh: %v", err)
	}

	// Подмена typ в claims перетирается подписывающей стороной.
	forged, err := h.SignAccess(ctx, map[string]any{"sub": "u", ClaimTokenType: TokenTypeRefresh})
	if err != nil {
		t.Fatalf("SignAccess: %v", err)
	}
	if _, err := h.ParseAccess(ctx, forged); err != nil {
		t.Fatalf("SignAccess must overwrite %s: %v", ClaimTokenType, err)
	}

	// Токен без typ (Sign) не проходит ни как access, ни как refresh.
	plain, err := h.Sign(ctx, map[string]any{"sub": "u"}, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := h.ParseAccess(ctx, plain); !errors.Is(err, ErrWrongTokenType) {
		t.Fatalf("untyped token accepted as access: %v", err)
	}
	if _, err := h.Parse(ctx, plain); err != nil {
		t.Fatalf("Parse must accept an untyped token: %v", err)
	}
}

func TestHS256JWT_ExpiredToken(t *testing.T) {
	ctx := t.Context()
	clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	h := newTestHS256(t, WithClock(clock))
	tok, err := h.Sign(ctx, map[string]any{"sub": "x"}, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	clock.Advance(3 * time.Minute) // за пределами leeway (60s по умолчанию)

	if _, err := h.Parse(ctx, tok); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestHS256JWT_Leeway(t *testing.T) {
	ctx := t.Context()
	clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	signer := newTestHS256(t, WithClock(clock))
	tok, err := signer.Sign(ctx, map[string]any{"sub": "x"}, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	clock.Advance(90 * time.Second) // 30s после exp

	lenient, err := NewHS256JWT(JWTConfig{Secret: testSecret, Leeway: time.Minute}, WithClock(clock))
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	if _, err := lenient.Parse(ctx, tok); err != nil {
		t.Fatalf("within leeway: %v", err)
	}
	strict, err := NewHS256JWT(JWTConfig{Secret: testSecret, Leeway: time.Second}, WithClock(clock))
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	if _, err := strict.Parse(ctx, tok); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("beyond leeway: expected ErrTokenExpired, got %v", err)
	}
}

func TestHS256JWT_WrongSignature(t *testing.T) {
	ctx := t.Context()
	signer := newTestHS256(t)
	tok, err := signer.Sign(ctx, map[string]any{"sub": "x"}, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	other, err := NewHS256JWT(JWTConfig{Secret: "other-secret-key-of-at-least-32-bytes!"})
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	if _, err := other.Parse(ctx, tok); !errors.Is(err, ErrTokenSignature) {
		t.Errorf("expected ErrTokenSignature, got %v", err)
	}
}

func TestHS256JWT_MalformedToken(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	cases := []string{"", "not.a.token", "only-one-part", "a.b.c"}
	for _, tc := range cases {
		if _, err := h.Parse(ctx, tc); !errors.Is(err, ErrTokenMalformed) {
			t.Errorf("input %q: expected ErrTokenMalformed, got %v", tc, err)
		}
	}
}

func TestHS256JWT_RejectsAlgNone(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x","exp":9999999999}`))
	for _, tok := range []string{header + "." + payload + ".", header + "." + payload} {
		if _, err := h.Parse(ctx, tok); !errors.Is(err, ErrTokenMalformed) {
			t.Errorf("alg none accepted: %v", err)
		}
	}
}

func TestHS256JWT_RejectsOtherHMACAlg(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS512, Key: bytes.Repeat([]byte("k"), 64)}, nil)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	tok, err := jwt.Signed(signer).Claims(map[string]any{"sub": "x", "exp": time.Now().Add(time.Hour).Unix()}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := h.Parse(ctx, tok); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("HS512 accepted: %v", err)
	}
}

func TestHS256JWT_RejectsRS256(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	tok, err := jwt.Signed(signer).Claims(map[string]any{"sub": "x", "exp": time.Now().Add(time.Hour).Unix()}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := h.Parse(ctx, tok); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("RS256 accepted: %v", err)
	}
}

func TestHS256JWT_RequiresExp(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	tok, err := jwt.Signed(h.signer).Claims(map[string]any{"sub": "x"}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := h.Parse(ctx, tok); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("token without exp accepted: %v", err)
	}
}

func TestHS256JWT_NotBeforeInFuture(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	tok, err := jwt.Signed(h.signer).Claims(map[string]any{
		"sub": "x",
		"exp": time.Now().Add(time.Hour).Unix(),
		"nbf": time.Now().Add(time.Hour).Unix(),
	}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := h.Parse(ctx, tok); !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("token with future nbf accepted: %v", err)
	}
}

// Подпись верна, но payload не разбирается в claims — это malformed, а не «ok».
func TestHS256JWT_NonObjectPayload(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	for name, payload := range map[string]string{
		"array":         `[1,2,3]`,
		"string exp":    `{"sub":"x","exp":"soon"}`,
		"not even json": `xyz`,
	} {
		t.Run(name, func(t *testing.T) {
			obj, err := h.signer.Sign([]byte(payload))
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			tok, err := obj.CompactSerialize()
			if err != nil {
				t.Fatalf("serialize: %v", err)
			}
			if _, err := h.Parse(ctx, tok); !errors.Is(err, ErrTokenMalformed) {
				t.Errorf("payload %q accepted: %v", payload, err)
			}
		})
	}
}

func TestHS256JWT_UnserializableClaims(t *testing.T) {
	ctx := t.Context()
	h := newTestHS256(t)
	if _, err := h.Sign(ctx, map[string]any{"c": make(chan int)}, time.Minute); err == nil {
		t.Fatal("expected an error on unserializable claims")
	}
}

// Общий секрет на несколько сервисов: чужой iss/aud не проходит.
func TestHS256JWT_IssuerAndAudience(t *testing.T) {
	ctx := t.Context()
	issued, err := NewHS256JWT(JWTConfig{Secret: testSecret}, WithIssuer("issuer-a"), WithAudience("svc-a"))
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	tok, err := issued.SignAccess(ctx, map[string]any{"sub": "u"})
	if err != nil {
		t.Fatalf("SignAccess: %v", err)
	}
	claims, err := issued.ParseAccess(ctx, tok)
	if err != nil {
		t.Fatalf("ParseAccess: %v", err)
	}
	if claims["iss"] != "issuer-a" || claims["aud"] != "svc-a" {
		t.Fatalf("iss=%v aud=%v", claims["iss"], claims["aud"])
	}

	otherAudience, err := NewHS256JWT(JWTConfig{Secret: testSecret}, WithAudience("svc-b"))
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	if _, err := otherAudience.Parse(ctx, tok); !errors.Is(err, ErrTokenAudience) {
		t.Fatalf("foreign audience accepted: %v", err)
	}

	otherIssuer, err := NewHS256JWT(JWTConfig{Secret: testSecret}, WithIssuer("issuer-b"))
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	if _, err := otherIssuer.Parse(ctx, tok); !errors.Is(err, ErrTokenAudience) {
		t.Fatalf("foreign issuer accepted: %v", err)
	}

	// Без опций проверка iss/aud не выполняется.
	if _, err := newTestHS256(t).Parse(ctx, tok); err != nil {
		t.Fatalf("parser without expectations: %v", err)
	}

	// Несколько audience — aud массивом, пересечение достаточно.
	multi, err := NewHS256JWT(JWTConfig{Secret: testSecret}, WithAudience("svc-a", "svc-b"))
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	multiTok, err := multi.SignAccess(ctx, map[string]any{"sub": "u"})
	if err != nil {
		t.Fatalf("SignAccess: %v", err)
	}
	if _, err := otherAudience.Parse(ctx, multiTok); err != nil {
		t.Fatalf("intersecting audience rejected: %v", err)
	}
}

// Опция применима только к своему конструктору: iss/aud, переданные в NewTOTP
// или в used-code store, ничего там не значат — компилятор обязан это поймать.
func TestOptions_ScopedToTheirConstructor(t *testing.T) {
	if _, ok := any(WithIssuer("x")).(TOTPOption); ok {
		t.Error("WithIssuer принимается NewTOTP")
	}
	if _, ok := any(WithAudience("x")).(TOTPOption); ok {
		t.Error("WithAudience принимается NewTOTP")
	}
	if _, ok := any(WithIssuer("x")).(UsedCodeStoreOption); ok {
		t.Error("WithIssuer принимается NewMemoryUsedCodeStore")
	}

	// WithClock(nil) не подсовывает nil-часы вместо clockkit.Real.
	store := NewMemoryUsedCodeStore(WithClock(nil))
	if fresh, err := store.MarkUsed(t.Context(), "k", time.Minute); !fresh || err != nil {
		t.Fatalf("MarkUsed: fresh=%v err=%v", fresh, err)
	}
	if _, err := NewHS256JWT(JWTConfig{Secret: testSecret}, WithClock(nil)); err != nil {
		t.Fatalf("NewHS256JWT с WithClock(nil): %v", err)
	}
	if _, err := NewTOTP(TOTPConfig{Issuer: "acme"}, WithClock(nil)); err != nil {
		t.Fatalf("NewTOTP с WithClock(nil): %v", err)
	}
}

var (
	_ JWTSigner = (*HS256JWT)(nil)
	_ JWTParser = (*HS256JWT)(nil)

	_ JWTOption           = WithClock(nil)
	_ TOTPOption          = WithClock(nil)
	_ UsedCodeStoreOption = WithClock(nil)
	_ JWTOption           = WithIssuer("")
	_ JWTOption           = WithAudience("")
)
