package hubtest_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
	"git.stit.tech/stit-core/golibinfra/testkit/hubtest"
)

const (
	otherEmployeeID = "01980000-0000-7000-8000-0000000000e2"
	otherCompanyID  = "01980000-0000-7000-8000-0000000000a2"
)

func claimsOf(t *testing.T, iss *hubtest.Issuer, raw string) map[string]any {
	t.Helper()
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	claims := map[string]any{}
	if err := tok.Claims(iss.PublicKey(), &claims); err != nil {
		t.Fatalf("verify token: %v", err)
	}
	return claims
}

func numClaim(t *testing.T, claims map[string]any, name string) int64 {
	t.Helper()
	v, ok := claims[name].(float64)
	if !ok {
		t.Fatalf("claim %s = %v (%T), want a number", name, claims[name], claims[name])
	}
	return int64(v)
}

func stringsClaim(t *testing.T, claims map[string]any, name string) []string {
	t.Helper()
	items, ok := claims[name].([]any)
	if !ok {
		t.Fatalf("claim %s = %v (%T), want an array", name, claims[name], claims[name])
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("claim %s element = %v (%T), want a string", name, item, item)
		}
		out = append(out, s)
	}
	return out
}

func TestTokenOptions_OverrideDefaultClaims(t *testing.T) {
	iss := hubtest.NewIssuer(t)

	tests := []struct {
		name  string
		token func(testing.TB, ...hubtest.TokenOption) string
		opt   hubtest.TokenOption
		claim string
		want  string
		dflt  string
	}{
		{"sid", iss.EmployeeToken, hubtest.WithSID("session-42"), sso.ClaimSID, "session-42", hubtest.DefaultSID},
		{"employee id", iss.EmployeeToken, hubtest.WithEmployeeID(otherEmployeeID), sso.ClaimEmployeeID, otherEmployeeID, hubtest.DefaultEmployeeID},
		{"company id", iss.CustomerToken, hubtest.WithCompanyID(otherCompanyID), sso.ClaimCompanyID, otherCompanyID, hubtest.DefaultCompanyID},
		{"account role", iss.CustomerToken, hubtest.WithAccountRole("admin"), sso.ClaimAccountRole, "admin", hubtest.DefaultAccountRole},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimsOf(t, iss, tc.token(t))[tc.claim]; got != tc.dflt {
				t.Fatalf("claim %s without the option = %v, want default %q", tc.claim, got, tc.dflt)
			}
			if got := claimsOf(t, iss, tc.token(t, tc.opt))[tc.claim]; got != tc.want {
				t.Fatalf("claim %s = %v, want %q", tc.claim, got, tc.want)
			}
		})
	}
}

func TestTokenOptions_CustomerIdentity(t *testing.T) {
	cust := hubtest.NewCustomerIssuer(t)
	tok := cust.CustomerToken(
		t,
		hubtest.WithSID("session-42"),
		hubtest.WithCompanyID(otherCompanyID),
		hubtest.WithAccountRole("admin"),
		hubtest.WithProducts("deal"),
	)

	claims := claimsOf(t, cust, tok)
	if got := stringsClaim(t, claims, sso.ClaimProducts); !slices.Equal(got, []string{"deal"}) {
		t.Fatalf("claim %s = %v, want [deal]", sso.ClaimProducts, got)
	}

	id, err := cust.CustomerVerifier(t).Verify(t.Context(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.SID != "session-42" || id.CompanyID != otherCompanyID || id.AccountRole != "admin" {
		t.Fatalf("identity = %+v", id)
	}
	if !slices.Equal(id.Products, []string{"deal"}) {
		t.Fatalf("products = %v, want [deal]", id.Products)
	}
}

func TestTokenOptions_WithClaims(t *testing.T) {
	iss := hubtest.NewIssuer(t)
	tok := iss.EmployeeToken(t, hubtest.WithClaims(map[string]any{
		sso.ClaimRole: "admin",
		"tenant":      "t-1",
		"aud":         "other-audience",
	}))

	claims := claimsOf(t, iss, tok)
	if claims[sso.ClaimRole] != "admin" || claims["tenant"] != "t-1" {
		t.Fatalf("claims = %v", claims)
	}
	if claims["aud"] != "other-audience" {
		t.Fatalf("aud = %v: WithClaims overrides standard claims too", claims["aud"])
	}
	if claims[sso.ClaimSID] != hubtest.DefaultSID || claims[sso.ClaimEmail] != hubtest.DefaultEmail {
		t.Fatalf("claims outside the set changed: %v", claims)
	}
	if _, err := iss.Verifier(t).Verify(t.Context(), tok); !errors.Is(err, sso.ErrInvalidClaims) {
		t.Fatalf("Verify with a foreign aud = %v, want %v", err, sso.ErrInvalidClaims)
	}
}

func TestTokenOptions_TTLAndIssuedAt(t *testing.T) {
	iss := hubtest.NewIssuer(t)

	def := claimsOf(t, iss, iss.EmployeeToken(t))
	if got, want := numClaim(t, def, "exp")-numClaim(t, def, "iat"), int64(hubtest.DefaultTokenTTL.Seconds()); got != want {
		t.Fatalf("exp-iat without options = %ds, want %ds", got, want)
	}

	issued := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	const ttl = 90 * time.Second
	tok := iss.EmployeeToken(t, hubtest.WithIssuedAt(issued), hubtest.WithTTL(ttl))

	claims := claimsOf(t, iss, tok)
	if got := numClaim(t, claims, "iat"); got != issued.Unix() {
		t.Fatalf("iat = %d, want %d", got, issued.Unix())
	}
	if got := numClaim(t, claims, "nbf"); got != issued.Unix() {
		t.Fatalf("nbf = %d, want %d", got, issued.Unix())
	}
	if got := numClaim(t, claims, "exp"); got != issued.Add(ttl).Unix() {
		t.Fatalf("exp = %d, want %d", got, issued.Add(ttl).Unix())
	}

	inside := iss.Verifier(t, sso.WithClock(clockkit.NewFixed(issued.Add(time.Minute))))
	if _, err := inside.Verify(t.Context(), tok); err != nil {
		t.Fatalf("Verify inside the token lifetime: %v", err)
	}
	after := iss.Verifier(t, sso.WithClock(clockkit.NewFixed(issued.Add(time.Hour))))
	if _, err := after.Verify(t.Context(), tok); !errors.Is(err, sso.ErrInvalidClaims) {
		t.Fatalf("Verify past exp = %v, want %v", err, sso.ErrInvalidClaims)
	}
	before := iss.Verifier(t, sso.WithClock(clockkit.NewFixed(issued.Add(-time.Hour))))
	if _, err := before.Verify(t.Context(), tok); !errors.Is(err, sso.ErrInvalidClaims) {
		t.Fatalf("Verify before nbf = %v, want %v", err, sso.ErrInvalidClaims)
	}
}
