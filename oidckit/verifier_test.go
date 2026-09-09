package oidckit_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/oidckit"
	"git.stit.tech/stit-core/golibinfra/testkit/oidctest"
)

const clientSecret = "client-secret-value-at-least-32-bytes"

func rpConfig(idp *oidctest.IDP) oidckit.RPConfig {
	return oidckit.RPConfig{
		Issuer:        idp.URL(),
		ClientID:      idp.ClientID(),
		ClientSecret:  clientSecret,
		RedirectURL:   "http://app.test/auth/callback",
		AllowInsecure: true,
	}
}

func newVerifier(t *testing.T, idp *oidctest.IDP) *oidckit.Verifier {
	t.Helper()
	v, err := oidckit.NewVerifier(t.Context(), rpConfig(idp), idp.Client())
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestVerify_Valid(t *testing.T) {
	idp := oidctest.New(t)
	idp.SetClaims(map[string]any{"groups": []any{"admins", "staff"}})
	v := newVerifier(t, idp)

	c, err := v.Verify(t.Context(), idp.IDToken(nil))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.Subject != oidctest.DefaultSubject || c.Username != oidctest.DefaultUsername || c.Email != oidctest.DefaultEmail {
		t.Fatalf("claims: %+v", c)
	}
	if len(c.Groups) != 2 || c.Groups[0] != "admins" {
		t.Fatalf("groups: %v", c.Groups)
	}
	if c.Raw["iss"] != idp.URL() {
		t.Fatalf("raw: %v", c.Raw)
	}
}

func TestVerify_ClaimErrors(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   error
	}{
		{"чужой aud", map[string]any{"aud": "other-app"}, nil},
		{"просрочен", map[string]any{"exp": time.Now().Add(-2 * time.Hour).Unix()}, nil},
		{"без exp", map[string]any{"exp": nil}, oidckit.ErrNoExpiry},
		{"без sub", map[string]any{"sub": ""}, oidckit.ErrNoSubject},
		{"чужой iss", map[string]any{"iss": "https://evil.example"}, nil},
		{"multi-aud без azp", map[string]any{"aud": []string{"test-client", "other"}}, oidckit.ErrAudience},
		{"azp другого клиента", map[string]any{"azp": "other"}, oidckit.ErrAudience},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := oidctest.New(t)
			v := newVerifier(t, idp)

			_, err := v.Verify(t.Context(), idp.IDToken(tc.claims))
			if err == nil {
				t.Fatalf("токен принят: %v", tc.claims)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("ожидали %v, получили %v", tc.want, err)
			}
		})
	}
}

// Несколько aud с корректным azp — валидный токен (OIDC Core §3.1.3.7).
func TestVerify_MultiAudienceWithAZP(t *testing.T) {
	idp := oidctest.New(t)
	v := newVerifier(t, idp)

	c, err := v.Verify(t.Context(), idp.IDToken(map[string]any{
		"aud": []string{idp.ClientID(), "other"},
		"azp": idp.ClientID(),
	}))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.Subject != oidctest.DefaultSubject {
		t.Fatalf("claims: %+v", c)
	}
}

func TestVerify_ForeignSignature(t *testing.T) {
	idp := oidctest.New(t)
	other := oidctest.New(t)
	v := newVerifier(t, idp)

	if _, err := v.Verify(t.Context(), other.IDToken(map[string]any{"iss": idp.URL()})); err == nil {
		t.Fatal("токен под чужой подписью прошёл проверку")
	}
}

// Симметричная подпись и alg=none отсекаются до чтения ключа.
func TestVerify_RejectsSymmetricAndNone(t *testing.T) {
	idp := oidctest.New(t)
	v := newVerifier(t, idp)

	for name, tok := range map[string]string{
		"HS256":    idp.IDTokenHS256(clientSecret, nil),
		"alg=none": idp.IDTokenUnsigned(nil),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(t.Context(), tok); !errors.Is(err, oidckit.ErrInvalidToken) {
				t.Fatalf("ожидали ErrInvalidToken, получили %v", err)
			}
		})
	}
}

func TestVerify_Leeway(t *testing.T) {
	idp := oidctest.New(t)
	now := time.Now()
	tok := idp.IDToken(map[string]any{"exp": now.Add(-30 * time.Second).Unix()})

	cases := map[string]struct {
		leeway  time.Duration
		wantErr bool
	}{
		"дефолтный leeway 1m покрывает 30s": {0, false},
		"leeway 1s не покрывает 30s":        {time.Second, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := rpConfig(idp)
			cfg.Leeway = tc.leeway
			cfg.Clock = clockkit.NewFixed(now)
			v, err := oidckit.NewVerifier(t.Context(), cfg, idp.Client())
			if err != nil {
				t.Fatalf("NewVerifier: %v", err)
			}
			_, err = v.Verify(t.Context(), tok)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, ожидали ошибку=%v", err, tc.wantErr)
			}
		})
	}
}

// Шторм токенов с неизвестным kid не превращается в шторм запросов к JWKS.
func TestVerify_UnknownKIDStorm_SingleJWKSFetch(t *testing.T) {
	idp := oidctest.New(t)
	v := newVerifier(t, idp)
	initial := idp.Hits(oidctest.EndpointJWKS)

	var wg sync.WaitGroup
	for n := range 50 {
		wg.Go(func() {
			tok := idp.IDTokenWithKID("unknown-"+strings.Repeat("x", n%3), nil)
			if _, err := v.Verify(t.Context(), tok); !errors.Is(err, oidckit.ErrUnknownKey) {
				t.Errorf("ожидали ErrUnknownKey, получили %v", err)
			}
		})
	}
	wg.Wait()
	if got := idp.Hits(oidctest.EndpointJWKS) - initial; got > 1 {
		t.Fatalf("50 неизвестных kid → %d загрузок JWKS, ожидали не больше 1", got)
	}
	if _, err := v.Verify(t.Context(), idp.IDToken(nil)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestNewVerifier_DiscoveryErrors(t *testing.T) {
	cases := []struct {
		name     string
		override map[string]any
		status   int
		want     error
	}{
		{"чужой issuer в документе", map[string]any{"issuer": "https://evil.example"}, 0, oidckit.ErrIssuerMismatch},
		{"нет jwks_uri", map[string]any{"jwks_uri": nil}, 0, oidckit.ErrNoJWKS},
		{"относительный jwks_uri", map[string]any{"jwks_uri": "/keys"}, 0, oidckit.ErrNoJWKS},
		{"нет token_endpoint", map[string]any{"token_endpoint": nil}, 0, oidckit.ErrNoEndpoints},
		{"относительный authorization_endpoint", map[string]any{"authorization_endpoint": "/authorize"}, 0, oidckit.ErrNoEndpoints},
		{"discovery недоступен", nil, 500, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := oidctest.New(t)
			if tc.override != nil {
				idp.SetDiscoveryOverride(tc.override)
			}
			if tc.status != 0 {
				idp.SetStatus(oidctest.EndpointDiscovery, tc.status)
			}

			_, err := oidckit.NewVerifier(t.Context(), rpConfig(idp), idp.Client())
			if err == nil {
				t.Fatal("NewVerifier прошёл на сломанном discovery")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("ожидали %v, получили %v", tc.want, err)
			}
		})
	}
}

func TestNewVerifier_JWKSUnavailable(t *testing.T) {
	idp := oidctest.New(t)
	idp.SetStatus(oidctest.EndpointJWKS, 503)
	if _, err := oidckit.NewVerifier(t.Context(), rpConfig(idp), idp.Client()); err == nil {
		t.Fatal("NewVerifier прошёл без загруженных ключей")
	}
}

func TestNewVerifier_Config(t *testing.T) {
	if _, err := oidckit.NewVerifier(context.Background(), oidckit.RPConfig{ClientID: "x"}, nil); !errors.Is(err, oidckit.ErrRPConfig) {
		t.Fatalf("пустой Issuer: ожидали ErrRPConfig, получили %v", err)
	}
}

// https-only по умолчанию: http-issuer принимается лишь с AllowInsecure.
func TestNewVerifier_InsecureEndpointsRejected(t *testing.T) {
	idp := oidctest.New(t)
	cfg := rpConfig(idp)
	cfg.AllowInsecure = false
	if _, err := oidckit.NewVerifier(t.Context(), cfg, idp.Client()); !errors.Is(err, oidckit.ErrNoJWKS) {
		t.Fatalf("http-endpoint принят: %v", err)
	}
}

func TestNewVerifier_NilDoer(t *testing.T) {
	idp := oidctest.New(t)
	v, err := oidckit.NewVerifier(t.Context(), rpConfig(idp), nil)
	if err != nil {
		t.Fatalf("NewVerifier с nil doer: %v", err)
	}
	if !v.KeysLoaded() {
		t.Fatal("ключи не загружены")
	}
}

func TestAuthCodeURL(t *testing.T) {
	idp := oidctest.New(t)
	v := newVerifier(t, idp)
	got := v.AuthCodeURL("state-1", "nonce-1", "challenge-1")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if !strings.HasPrefix(got, idp.URL()+"/authorize?") {
		t.Fatalf("prefix: %s", got)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": idp.ClientID(), "redirect_uri": "http://app.test/auth/callback", "response_type": "code",
		"state": "state-1", "nonce": "nonce-1", "code_challenge": "challenge-1", "code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Fatalf("%s=%q, ожидали %q", k, q.Get(k), want)
		}
	}
}

// authorization_endpoint с собственным query дополняется, а не ломается.
func TestAuthCodeURL_EndpointWithQuery(t *testing.T) {
	idp := oidctest.New(t)
	idp.SetDiscoveryOverride(map[string]any{"authorization_endpoint": idp.URL() + "/authorize?tenant=acme"})
	v := newVerifier(t, idp)

	u, err := url.Parse(v.AuthCodeURL("s", "n", "c"))
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Query().Get("tenant") != "acme" || u.Query().Get("state") != "s" {
		t.Fatalf("query=%v", u.Query())
	}
	if strings.Count(u.String(), "?") != 1 {
		t.Fatalf("сломанный URL: %s", u)
	}
}

func TestExchange(t *testing.T) {
	idp := oidctest.New(t)
	v := newVerifier(t, idp)
	code := authorize(t, idp, v)

	c, err := v.Exchange(t.Context(), code, pkceVerifier, idp.LastAuthorize().Nonce)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if c.Subject != oidctest.DefaultSubject {
		t.Fatalf("claims: %+v", c)
	}
}

func TestExchange_NonceMismatch(t *testing.T) {
	idp := oidctest.New(t)
	v := newVerifier(t, idp)
	code := authorize(t, idp, v)

	if _, err := v.Exchange(t.Context(), code, pkceVerifier, "nonce-expected"); !errors.Is(err, oidckit.ErrNonceMismatch) {
		t.Fatalf("ожидали ErrNonceMismatch, получили %v", err)
	}
}

func TestExchange_Errors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"token endpoint 500", 500, "", oidckit.ErrExchange},
		{"ответ без id_token", 0, `{"token_type":"Bearer"}`, oidckit.ErrNoIDToken},
		{"невалидный JSON", 0, `{`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := oidctest.New(t)
			v := newVerifier(t, idp)
			if tc.status != 0 {
				idp.SetStatus(oidctest.EndpointToken, tc.status)
			}
			idp.SetTokenBody(tc.body)

			_, err := v.Exchange(t.Context(), "code", "verifier", "")
			if err == nil {
				t.Fatal("Exchange прошёл на сломанном ответе")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("ожидали %v, получили %v", tc.want, err)
			}
		})
	}
}

func TestPKCE(t *testing.T) {
	verifier, challenge, err := oidckit.PKCE()
	if err != nil {
		t.Fatalf("PKCE: %v", err)
	}
	if challenge == "" || verifier == challenge {
		t.Fatalf("verifier=%q challenge=%q", verifier, challenge)
	}
	other, _, _ := oidckit.PKCE()
	if verifier == other {
		t.Fatal("PKCE verifier не случаен")
	}
}

func TestRandomToken(t *testing.T) {
	a, err := oidckit.RandomToken()
	if err != nil {
		t.Fatalf("RandomToken: %v", err)
	}
	b, _ := oidckit.RandomToken()
	if a == b || len(a) < 40 {
		t.Fatalf("a=%q b=%q", a, b)
	}
}

// pkceVerifier/authorize проводят реальный /authorize stub-провайдера, чтобы
// Exchange получал code, привязанный к nonce и code_challenge.
const pkceVerifier = "test-code-verifier-value"

func authorize(t *testing.T, idp *oidctest.IDP, v *oidckit.Verifier) string {
	t.Helper()
	challenge := pkceChallenge(t)
	client := idp.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Get(v.AuthCodeURL("state-1", "nonce-1", challenge))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("stub не выдал code: %s", resp.Header.Get("Location"))
	}
	return code
}

func pkceChallenge(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(pkceVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
