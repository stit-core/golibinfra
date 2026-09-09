package oidctest_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/testkit/oidctest"
)

func numClaim(t *testing.T, claims map[string]any, name string) int64 {
	t.Helper()
	v, ok := claims[name].(float64)
	if !ok {
		t.Fatalf("claim %s = %v (%T), want a number", name, claims[name], claims[name])
	}
	return int64(v)
}

// authorizeCode проходит /authorize и возвращает выданный code.
func authorizeCode(t *testing.T, idp *oidctest.IDP) string {
	t.Helper()
	q := url.Values{
		"client_id": {idp.ClientID()}, "redirect_uri": {"http://app.test/auth/callback"},
		"response_type": {"code"}, "state": {"state-1"},
	}
	resp, err := noRedirect(idp.Client()).Get(idp.URL() + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("authorize не выдал code: %s", loc)
	}
	return code
}

func tokenResponse(t *testing.T, idp *oidctest.IDP, code string) map[string]any {
	t.Helper()
	resp, err := idp.Client().PostForm(idp.URL()+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {idp.ClientID()},
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	return body
}

func TestIdP_Options(t *testing.T) {
	const (
		kid = "option-kid"
		sub = "option-subject"
		ttl = 90 * time.Second
	)
	idp := oidctest.New(
		t,
		oidctest.WithKID(kid),
		oidctest.WithSubject(sub),
		oidctest.WithTokenTTL(ttl),
		oidctest.WithClaims(map[string]any{"email": "opt@example.test", "groups": []string{"admins"}}),
	)

	if ks := keySet(t, idp); len(ks.Keys) != 1 || ks.Keys[0].KeyID != kid {
		t.Fatalf("jwks: %+v", ks.Keys)
	}
	raw := idp.IDToken(nil)
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := tok.Headers[0].KeyID; got != kid {
		t.Fatalf("kid заголовка = %q, want %q", got, kid)
	}

	claims := parse(t, idp, raw)
	if claims["sub"] != sub {
		t.Fatalf("sub = %v, want %q", claims["sub"], sub)
	}
	if claims["email"] != "opt@example.test" {
		t.Fatalf("email = %v: набор перекрывает дефолтный claim", claims["email"])
	}
	if claims["preferred_username"] != oidctest.DefaultUsername {
		t.Fatalf("preferred_username = %v: дефолты вне набора не тронуты", claims["preferred_username"])
	}
	groups, ok := claims["groups"].([]any)
	if !ok || !slices.Equal(groups, []any{"admins"}) {
		t.Fatalf("groups = %v", claims["groups"])
	}
	if got := numClaim(t, claims, "exp") - numClaim(t, claims, "iat"); got != int64(ttl.Seconds()) {
		t.Fatalf("exp-iat = %ds, want %ds", got, int64(ttl.Seconds()))
	}

	body := tokenResponse(t, idp, authorizeCode(t, idp))
	if got := body["expires_in"]; got != float64(ttl.Seconds()) {
		t.Fatalf("expires_in = %v, want %v", got, ttl.Seconds())
	}
	if got := parse(t, idp, body["id_token"].(string)); got["sub"] != sub {
		t.Fatalf("sub токена с /token = %v, want %q", got["sub"], sub)
	}
}

func TestIdP_SetSubject(t *testing.T) {
	idp := oidctest.New(t)
	if got := parse(t, idp, idp.IDToken(nil))["sub"]; got != oidctest.DefaultSubject {
		t.Fatalf("sub = %v, want %q", got, oidctest.DefaultSubject)
	}

	idp.SetSubject("second-subject")
	if got := parse(t, idp, idp.IDToken(nil))["sub"]; got != "second-subject" {
		t.Fatalf("sub после SetSubject = %v", got)
	}
	if got := parse(t, idp, tokenResponse(t, idp, authorizeCode(t, idp))["id_token"].(string))["sub"]; got != "second-subject" {
		t.Fatalf("sub токена с /token = %v", got)
	}
	if got := parse(t, idp, idp.IDToken(map[string]any{"sub": "third-subject"}))["sub"]; got != "third-subject" {
		t.Fatalf("sub = %v: явный override сильнее SetSubject", got)
	}
}

func TestIdP_SetDiscoveryOverride(t *testing.T) {
	idp := oidctest.New(t)
	if _, ok := discovery(t, idp)["jwks_uri"]; !ok {
		t.Fatal("штатный документ без jwks_uri")
	}

	idp.SetDiscoveryOverride(map[string]any{
		"token_endpoint": "https://elsewhere.test/token",
		"custom_field":   "x",
		"jwks_uri":       nil,
	})

	doc := discovery(t, idp)
	if doc["token_endpoint"] != "https://elsewhere.test/token" {
		t.Fatalf("token_endpoint = %v", doc["token_endpoint"])
	}
	if doc["custom_field"] != "x" {
		t.Fatalf("custom_field = %v", doc["custom_field"])
	}
	if _, ok := doc["jwks_uri"]; ok {
		t.Fatalf("nil-override не удалил jwks_uri: %v", doc)
	}
	if doc["issuer"] != idp.URL() || doc["authorization_endpoint"] != idp.URL()+"/authorize" {
		t.Fatalf("нетронутые поля документа изменились: %v", doc)
	}
}

func TestIdP_Hits(t *testing.T) {
	idp := oidctest.New(t)
	endpoints := []oidctest.Endpoint{
		oidctest.EndpointDiscovery, oidctest.EndpointJWKS,
		oidctest.EndpointAuthorize, oidctest.EndpointToken,
	}
	for _, ep := range endpoints {
		if got := idp.Hits(ep); got != 0 {
			t.Fatalf("Hits(%s) на свежем stub'е = %d, want 0", ep, got)
		}
	}

	discovery(t, idp)
	keySet(t, idp)
	tokenResponse(t, idp, authorizeCode(t, idp))
	for _, ep := range endpoints {
		if got := idp.Hits(ep); got != 1 {
			t.Fatalf("Hits(%s) = %d, want 1", ep, got)
		}
	}

	idp.SetStatus(oidctest.EndpointJWKS, http.StatusServiceUnavailable)
	resp, err := idp.Client().Get(idp.JWKSURL())
	if err != nil {
		t.Fatalf("jwks: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := idp.Hits(oidctest.EndpointJWKS); got != 2 {
		t.Fatalf("Hits(jwks) = %d: запрос, отбитый подстановкой, тоже считается", got)
	}
}
