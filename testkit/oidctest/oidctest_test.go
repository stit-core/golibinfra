package oidctest_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/testkit/oidctest"
)

// noRedirect — редиректы разбираем сами: нас интересует Location, а не тело.
func noRedirect(c *http.Client) *http.Client {
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

func discovery(t *testing.T, idp *oidctest.IDP) map[string]any {
	t.Helper()
	resp, err := idp.Client().Get(idp.URL() + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	return doc
}

func keySet(t *testing.T, idp *oidctest.IDP) jose.JSONWebKeySet {
	t.Helper()
	resp, err := idp.Client().Get(idp.JWKSURL())
	if err != nil {
		t.Fatalf("jwks: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var ks jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&ks); err != nil {
		t.Fatalf("decode jwks: %v", err)
	}
	return ks
}

func TestIdP_DiscoveryAndJWKS(t *testing.T) {
	idp := oidctest.New(t)
	doc := discovery(t, idp)

	if doc["issuer"] != idp.URL() {
		t.Fatalf("issuer=%v, ожидали %s", doc["issuer"], idp.URL())
	}
	if doc["jwks_uri"] != idp.JWKSURL() {
		t.Fatalf("jwks_uri=%v", doc["jwks_uri"])
	}
	ks := keySet(t, idp)
	if len(ks.Keys) != 1 || ks.Keys[0].KeyID != oidctest.DefaultKID || ks.Keys[0].Use != "sig" {
		t.Fatalf("jwks: %+v", ks.Keys)
	}
}

// Полный code-flow: /authorize отдаёт code на redirect_uri, /token меняет его на
// подписанный ID-token с эхом nonce.
func TestIdP_CodeFlow(t *testing.T) {
	idp := oidctest.New(t)
	verifier := "code-verifier-value"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	q := url.Values{
		"client_id": {idp.ClientID()}, "redirect_uri": {"http://app.test/auth/callback"},
		"response_type": {"code"}, "state": {"state-1"}, "nonce": {"nonce-1"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	resp, err := noRedirect(idp.Client()).Get(idp.URL() + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status=%d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	code := loc.Query().Get("code")
	if code == "" || loc.Query().Get("state") != "state-1" {
		t.Fatalf("location=%s", loc)
	}
	if got := idp.LastAuthorize(); got.Nonce != "nonce-1" || got.CodeChallengeMethod != "S256" {
		t.Fatalf("LastAuthorize=%+v", got)
	}

	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"code_verifier": {verifier}, "client_id": {idp.ClientID()},
	}
	tokenResp, err := idp.Client().PostForm(idp.URL()+"/token", form)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer func() { _ = tokenResp.Body.Close() }()
	var tr struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tr); err != nil {
		t.Fatalf("decode token: %v", err)
	}

	claims := parse(t, idp, tr.IDToken)
	if claims["nonce"] != "nonce-1" || claims["sub"] != oidctest.DefaultSubject {
		t.Fatalf("claims=%v", claims)
	}
	if claims["iss"] != idp.URL() || claims["aud"] != idp.ClientID() {
		t.Fatalf("claims=%v", claims)
	}
}

func TestIdP_CodeFlow_WrongVerifier(t *testing.T) {
	idp := oidctest.New(t)
	q := url.Values{
		"client_id": {idp.ClientID()}, "redirect_uri": {"http://app.test/cb"},
		"response_type": {"code"}, "code_challenge": {"challenge-that-wont-match"},
		"code_challenge_method": {"S256"},
	}
	resp, err := noRedirect(idp.Client()).Get(idp.URL() + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	_ = resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))

	tokenResp, err := idp.Client().PostForm(idp.URL()+"/token", url.Values{
		"code": {loc.Query().Get("code")}, "code_verifier": {"nope"},
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer func() { _ = tokenResp.Body.Close() }()
	if tokenResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("token status=%d, ожидали 400", tokenResp.StatusCode)
	}
}

func TestIdP_FaultInjection(t *testing.T) {
	idp := oidctest.New(t)
	idp.SetStatus(oidctest.EndpointDiscovery, http.StatusInternalServerError)
	resp, err := idp.Client().Get(idp.URL() + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	idp.SetStatus(oidctest.EndpointDiscovery, 0)
	if doc := discovery(t, idp); doc["issuer"] != idp.URL() {
		t.Fatalf("подстановка не снята: %v", doc)
	}

	idp.SetTokenBody(`{"token_type":"Bearer"}`)
	tokenResp, err := idp.Client().PostForm(idp.URL()+"/token", url.Values{"code": {"x"}})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer func() { _ = tokenResp.Body.Close() }()
	var tr map[string]any
	if err := json.NewDecoder(tokenResp.Body).Decode(&tr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := tr["id_token"]; ok {
		t.Fatalf("подменённое тело содержит id_token: %v", tr)
	}
}

func TestIdP_ES256AndClaimOverrides(t *testing.T) {
	idp := oidctest.New(t, oidctest.WithAlgorithm(jose.ES256), oidctest.WithClientID("svc"))
	idp.SetClaims(map[string]any{"groups": []string{"admins"}})

	tok := idp.IDToken(map[string]any{"exp": nil, "sub": "other"})
	claims := parse(t, idp, tok)
	if _, ok := claims["exp"]; ok {
		t.Fatalf("nil-override не удалил exp: %v", claims)
	}
	if claims["sub"] != "other" || claims["aud"] != "svc" {
		t.Fatalf("claims=%v", claims)
	}
	if ks := keySet(t, idp); ks.Keys[0].Algorithm != string(jose.ES256) {
		t.Fatalf("alg=%s", ks.Keys[0].Algorithm)
	}
}

func TestIdP_UnsafeTokenHelpers(t *testing.T) {
	idp := oidctest.New(t)

	hs := idp.IDTokenHS256(strings.Repeat("client-secret-", 3), nil)
	if _, err := jwt.ParseSigned(hs, []jose.SignatureAlgorithm{jose.RS256}); err == nil {
		t.Fatal("HS256-токен принят разбором с allowlist RS256")
	}
	if _, err := jwt.ParseSigned(hs, []jose.SignatureAlgorithm{jose.HS256}); err != nil {
		t.Fatalf("HS256-токен не разобран своим алгоритмом: %v", err)
	}

	none := idp.IDTokenUnsigned(nil)
	if parts := strings.Split(none, "."); len(parts) != 3 || parts[2] != "" {
		t.Fatalf("alg=none токен: %q", none)
	}
	if _, err := jwt.ParseSigned(none, []jose.SignatureAlgorithm{jose.RS256}); err == nil {
		t.Fatal("токен alg=none прошёл разбор")
	}

	unknown := idp.IDTokenWithKID("other-kid", nil)
	tok, err := jwt.ParseSigned(unknown, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tok.Headers[0].KeyID != "other-kid" {
		t.Fatalf("kid=%s", tok.Headers[0].KeyID)
	}
}

func parse(t *testing.T, idp *oidctest.IDP, raw string) map[string]any {
	t.Helper()
	ks := keySet(t, idp)
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	claims := map[string]any{}
	if err := tok.Claims(ks.Keys[0].Key, &claims); err != nil {
		t.Fatalf("verify token: %v", err)
	}
	return claims
}
