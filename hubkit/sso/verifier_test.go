package sso_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
	"git.stit.tech/stit-core/golibinfra/testkit/hubtest"
)

func TestVerify_Valid(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	id, err := iss.Verifier(t).Verify(t.Context(), iss.EmployeeToken(t))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Subject != hubtest.DefaultSubject || id.EmployeeID != hubtest.DefaultEmployeeID {
		t.Fatalf("identity: %+v", id)
	}
	if id.Kind != sso.KindEmployee {
		t.Fatalf("kind = %q, want employee", id.Kind)
	}
}

func TestVerify_RejectsNonES256(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	v := iss.Verifier(t)

	// Key confusion: публичный ключ issuer'а как HMAC-секрет.
	hs256 := iss.SignHS256(t, map[string]any{"sub": "attacker"})
	if _, err := v.Verify(t.Context(), hs256); !errors.Is(err, sso.ErrInvalidToken) {
		t.Fatalf("HS256 err = %v, want ErrInvalidToken", err)
	}

	if _, err := v.Verify(t.Context(), unsignedToken(t, iss)); !errors.Is(err, sso.ErrInvalidToken) {
		t.Fatalf("alg=none err = %v, want ErrInvalidToken", err)
	}
}

// unsignedToken собирает JWS с alg=none и пустой подписью.
func unsignedToken(t *testing.T, iss *hubtest.Issuer) string {
	t.Helper()
	enc := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	header := enc(map[string]any{"alg": "none", "typ": "JWT"})
	payload := enc(map[string]any{
		"iss": iss.IssuerURL(),
		"aud": []string{iss.Audience()},
		"sub": "attacker",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return header + "." + payload + "."
}

func TestVerify_ForeignKey(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	other := hubtest.NewIssuer(t)
	// Тот же kid, чужой ключ: verifier обязан отвергнуть по подписи, а не по kid.
	token := other.Sign(t, map[string]any{"sub": "u1"}, hubtest.WithKID(iss.KID()))
	if _, err := iss.Verifier(t).Verify(t.Context(), token); err == nil {
		t.Fatal("foreign key accepted")
	}
}

func TestVerify_Expired(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	token := iss.EmployeeToken(t, hubtest.WithIssuedAt(time.Now().Add(-2*time.Hour)), hubtest.WithTTL(time.Hour))
	_, err := iss.Verifier(t).Verify(t.Context(), token)
	if !errors.Is(err, sso.ErrInvalidClaims) {
		t.Fatalf("err = %v, want ErrInvalidClaims", err)
	}
}

func TestVerify_NoExpiry(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	token := iss.EmployeeToken(t, hubtest.WithoutClaim("exp"))
	if _, err := iss.Verifier(t).Verify(t.Context(), token); !errors.Is(err, sso.ErrNoExpiry) {
		t.Fatalf("err = %v, want ErrNoExpiry", err)
	}
}

func TestVerify_NoSubject(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	token := iss.EmployeeToken(t, hubtest.WithoutClaim("sub"))
	if _, err := iss.Verifier(t).Verify(t.Context(), token); !errors.Is(err, sso.ErrNoSubject) {
		t.Fatalf("err = %v, want ErrNoSubject", err)
	}
}

func TestVerify_LeewayFromConfig(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	clock := clockkit.NewFixed(time.Now())
	// Токен истёк 30s назад: leeway 60s его принимает, нулевой — нет.
	token := iss.EmployeeToken(t, hubtest.WithExpiry(clock.Now().Add(-30*time.Second)))

	cfg := hubtest.SSOConfig(iss, nil)
	lenient, err := sso.NewVerifier(cfg, nil, sso.WithClock(clock))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := lenient.Verify(t.Context(), token); err != nil {
		t.Fatalf("leeway 60s rejected token: %v", err)
	}

	cfg.Leeway = 0
	strict, err := sso.NewVerifier(cfg, nil, sso.WithClock(clock))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := strict.Verify(t.Context(), token); !errors.Is(err, sso.ErrInvalidClaims) {
		t.Fatalf("leeway 0 err = %v, want ErrInvalidClaims", err)
	}
}

func TestVerify_UnknownKID(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	token := iss.EmployeeToken(t, hubtest.WithKID("does-not-exist"))
	if _, err := iss.Verifier(t).Verify(t.Context(), token); !errors.Is(err, sso.ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}

func TestVerify_UnknownKIDStorm_SingleJWKSFetch(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	v := iss.Verifier(t)
	if _, err := v.Verify(t.Context(), iss.EmployeeToken(t)); err != nil {
		t.Fatalf("warm up: %v", err)
	}
	before := iss.JWKSRequests()

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, _ = v.Verify(t.Context(), iss.EmployeeToken(t, hubtest.WithKID("kid-"+string(rune('a'+i%20)))))
		})
	}
	wg.Wait()

	if got := iss.JWKSRequests() - before; got > 1 {
		t.Fatalf("jwks fetches during storm = %d, want <= 1", got)
	}
}

func TestVerify_RotationKeepsLiveTokensValid(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	clock := clockkit.NewTickable(time.Now())
	v, err := sso.NewVerifier(hubtest.SSOConfig(iss, nil), nil, sso.WithClock(clock))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	live := iss.EmployeeToken(t)
	if _, err := v.Verify(t.Context(), live); err != nil {
		t.Fatalf("before rotation: %v", err)
	}

	// Ротация выкидывает прежний kid из набора; grace-период previous-слота
	// обязан удержать уже выданные access-токены.
	iss.Rotate(t)
	clock.Advance(6 * time.Minute) // окно свежести JWKS (Cache-Control: max-age=300)

	if _, err := v.Verify(t.Context(), iss.EmployeeToken(t)); err != nil {
		t.Fatalf("new key token: %v", err)
	}
	if _, err := v.Verify(t.Context(), live); err != nil {
		t.Fatalf("token of previous kid rejected after rotation: %v", err)
	}
}

func TestVerify_JWKSUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	iss := hubtest.NewIssuer(t)
	cfg := hubtest.SSOConfig(iss, nil)
	cfg.JWKSURL = srv.URL
	v, err := sso.NewVerifier(cfg, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := v.Verify(t.Context(), iss.EmployeeToken(t)); !errors.Is(err, sso.ErrJWKSUnavailable) {
		t.Fatalf("err = %v, want ErrJWKSUnavailable", err)
	}
}

func TestVerify_WrongIssuerAndAudience(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	v := iss.Verifier(t)
	for name, token := range map[string]string{
		"issuer":   iss.EmployeeToken(t, hubtest.WithClaim("iss", "https://evil.test")),
		"audience": iss.EmployeeToken(t, hubtest.WithClaim("aud", []string{"other"})),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(t.Context(), token); !errors.Is(err, sso.ErrInvalidClaims) {
				t.Fatalf("err = %v, want ErrInvalidClaims", err)
			}
		})
	}
}

func TestVerify_Malformed(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	if _, err := iss.Verifier(t).Verify(t.Context(), "not-a-jwt"); !errors.Is(err, sso.ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestVerifier_Refresh(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	if err := iss.Verifier(t).Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if iss.JWKSRequests() == 0 {
		t.Fatal("Refresh did not hit the JWKS endpoint")
	}
}

func TestVerifier_KindAccessor(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	if got := iss.Verifier(t).Kind(); got != sso.KindEmployee {
		t.Fatalf("Kind() = %q", got)
	}
}
