// Package oidctest — stub OIDC-провайдера для тестов потребителей oidckit:
// discovery, JWKS, /authorize и /token с настраиваемыми claims, ключами
// RS256/ES256 и подстановкой ошибок на любом endpoint'е.
package oidctest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Значения по умолчанию, которые stub кладёт в выдаваемые токены.
const (
	DefaultKID      = "oidctest-1"
	DefaultClientID = "test-client"
	DefaultSubject  = "test-subject"
	DefaultUsername = "j.doe"
	DefaultName     = "Jane Doe"
	DefaultEmail    = "j.doe@example.test"
	DefaultTokenTTL = time.Hour
)

// Endpoint — endpoint stub-провайдера (для подстановки ошибок).
type Endpoint string

// Endpoint* — адресуемые endpoint'ы stub-провайдера.
const (
	EndpointDiscovery Endpoint = "discovery"
	EndpointJWKS      Endpoint = "jwks"
	EndpointAuthorize Endpoint = "authorize"
	EndpointToken     Endpoint = "token"
)

// AuthorizeRequest — параметры, с которыми relying-party пришла на /authorize.
type AuthorizeRequest struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string
	Scope               string
	State               string
	Nonce               string
	CodeChallenge       string
	CodeChallengeMethod string
}

// Option — настройка stub-провайдера, передаётся в New.
type Option func(*IDP)

// WithAlgorithm выбирает алгоритм подписи ID-token'ов: RS256 (default) или ES256.
func WithAlgorithm(alg jose.SignatureAlgorithm) Option {
	return func(i *IDP) { i.alg = alg }
}

// WithClientID задаёт client_id, для которого выпускаются токены (aud).
func WithClientID(clientID string) Option {
	return func(i *IDP) { i.clientID = clientID }
}

// WithSubject задаёт sub выдаваемых токенов.
func WithSubject(sub string) Option {
	return func(i *IDP) { i.subject = sub }
}

// WithClaims добавляет claims в каждый выдаваемый ID-token (поверх дефолтных).
func WithClaims(claims map[string]any) Option {
	return func(i *IDP) { maps.Copy(i.claims, claims) }
}

// WithTokenTTL задаёт срок жизни выдаваемых токенов (exp).
func WithTokenTTL(ttl time.Duration) Option {
	return func(i *IDP) { i.tokenTTL = ttl }
}

// WithKID задаёт kid ключа подписи (он же единственный ключ в JWKS).
func WithKID(kid string) Option {
	return func(i *IDP) { i.kid = kid }
}

// IDP — работающий stub OIDC-провайдера на httptest.Server; закрывается через
// t.Cleanup.
type IDP struct {
	tb       testing.TB
	srv      *httptest.Server
	alg      jose.SignatureAlgorithm
	kid      string
	clientID string
	tokenTTL time.Duration
	priv     crypto.Signer
	jwks     jose.JSONWebKeySet

	mu        sync.Mutex
	subject   string
	claims    map[string]any
	status    map[Endpoint]int
	hits      map[Endpoint]int
	discovery map[string]any
	tokenBody string
	codes     map[string]AuthorizeRequest
	last      AuthorizeRequest
}

// New поднимает stub-провайдера и возвращает его; сервер закрывается вместе с тестом.
func New(tb testing.TB, opts ...Option) *IDP {
	tb.Helper()
	i := &IDP{
		tb:       tb,
		alg:      jose.RS256,
		kid:      DefaultKID,
		clientID: DefaultClientID,
		subject:  DefaultSubject,
		tokenTTL: DefaultTokenTTL,
		claims: map[string]any{
			"preferred_username": DefaultUsername,
			"name":               DefaultName,
			"email":              DefaultEmail,
		},
		status:    map[Endpoint]int{},
		hits:      map[Endpoint]int{},
		discovery: map[string]any{},
		codes:     map[string]AuthorizeRequest{},
	}
	for _, o := range opts {
		o(i)
	}
	i.genKey()

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", i.handleDiscovery)
	mux.HandleFunc("/jwks", i.handleJWKS)
	mux.HandleFunc("/authorize", i.handleAuthorize)
	mux.HandleFunc("/token", i.handleToken)
	i.srv = httptest.NewServer(mux)
	tb.Cleanup(i.srv.Close)
	return i
}

// URL — issuer stub-провайдера (он же базовый URL всех его endpoint'ов).
func (i *IDP) URL() string { return i.srv.URL }

// JWKSURL — адрес набора ключей.
func (i *IDP) JWKSURL() string { return i.srv.URL + "/jwks" }

// ClientID — client_id, для которого stub выпускает токены.
func (i *IDP) ClientID() string { return i.clientID }

// Client — http.Client, умеющий ходить в stub.
func (i *IDP) Client() *http.Client { return i.srv.Client() }

// SetSubject меняет sub последующих токенов.
func (i *IDP) SetSubject(sub string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.subject = sub
}

// SetClaims добавляет (или переопределяет) claims последующих токенов.
func (i *IDP) SetClaims(claims map[string]any) {
	i.mu.Lock()
	defer i.mu.Unlock()
	maps.Copy(i.claims, claims)
}

// SetStatus заставляет endpoint отвечать заданным статусом; 0 — снять подстановку.
func (i *IDP) SetStatus(ep Endpoint, status int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if status == 0 {
		delete(i.status, ep)
		return
	}
	i.status[ep] = status
}

// SetTokenBody подменяет тело ответа token endpoint'а (ответ без id_token,
// невалидный JSON); пустая строка — вернуть штатное поведение.
func (i *IDP) SetTokenBody(body string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tokenBody = body
}

// SetDiscoveryOverride правит discovery-документ: значения дополняют и
// переопределяют штатные поля, nil — удаляет поле (документ без jwks_uri).
func (i *IDP) SetDiscoveryOverride(fields map[string]any) {
	i.mu.Lock()
	defer i.mu.Unlock()
	maps.Copy(i.discovery, fields)
}

// Hits — сколько запросов пришло на endpoint (проверка троттлинга и кэшей).
func (i *IDP) Hits(ep Endpoint) int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.hits[ep]
}

// LastAuthorize — параметры последнего запроса к /authorize.
func (i *IDP) LastAuthorize() AuthorizeRequest {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.last
}

// IDToken подписывает ID-token ключом stub'а. claims дополняют и переопределяют
// дефолтные iss/aud/sub/exp/iat; значение nil удаляет claim (токен без exp).
func (i *IDP) IDToken(claims map[string]any) string {
	return i.IDTokenWithKID(i.kid, claims)
}

// IDTokenWithKID — IDToken, подписанный с чужим kid в заголовке (проверка
// поведения при неизвестном ключе).
func (i *IDP) IDTokenWithKID(kid string, claims map[string]any) string {
	i.tb.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: i.alg, Key: i.priv},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		i.tb.Fatalf("oidctest: signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(i.tokenClaims(claims)).Serialize()
	if err != nil {
		i.tb.Fatalf("oidctest: sign id token: %v", err)
	}
	return raw
}

// IDTokenHS256 подписывает те же claims симметричным ключом: relying-party
// обязан отвергнуть такой токен (он подделывается держателем client secret'а).
// Секрет — не короче 32 байт (требование HS256 к длине ключа).
func (i *IDP) IDTokenHS256(secret string, claims map[string]any) string {
	i.tb.Helper()
	if len(secret) < sha256.Size {
		i.tb.Fatalf("oidctest: HS256 secret must be at least %d bytes", sha256.Size)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: []byte(secret)},
		(&jose.SignerOptions{}).WithHeader("kid", i.kid).WithType("JWT"),
	)
	if err != nil {
		i.tb.Fatalf("oidctest: hs256 signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(i.tokenClaims(claims)).Serialize()
	if err != nil {
		i.tb.Fatalf("oidctest: sign id token: %v", err)
	}
	return raw
}

// IDTokenUnsigned собирает токен с alg=none: relying-party обязан его отвергнуть.
func (i *IDP) IDTokenUnsigned(claims map[string]any) string {
	i.tb.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT", "kid": i.kid})
	if err != nil {
		i.tb.Fatalf("oidctest: header: %v", err)
	}
	payload, err := json.Marshal(i.tokenClaims(claims))
	if err != nil {
		i.tb.Fatalf("oidctest: payload: %v", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(header) + "." + enc.EncodeToString(payload) + "."
}

func (i *IDP) tokenClaims(override map[string]any) map[string]any {
	i.mu.Lock()
	defer i.mu.Unlock()
	now := time.Now()
	claims := map[string]any{
		"iss": i.srv.URL,
		"aud": i.clientID,
		"sub": i.subject,
		"exp": now.Add(i.tokenTTL).Unix(),
		"iat": now.Unix(),
	}
	maps.Copy(claims, i.claims)
	for k, v := range override {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	return claims
}

func (i *IDP) genKey() {
	i.tb.Helper()
	switch i.alg {
	case jose.ES256:
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			i.tb.Fatalf("oidctest: ecdsa key: %v", err)
		}
		i.priv = key
	case jose.RS256:
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			i.tb.Fatalf("oidctest: rsa key: %v", err)
		}
		i.priv = key
	default:
		i.tb.Fatalf("oidctest: unsupported algorithm %q (RS256 or ES256)", i.alg)
	}
	i.jwks = jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: i.priv.Public(), KeyID: i.kid, Algorithm: string(i.alg), Use: "sig",
	}}}
}

func (i *IDP) failed(w http.ResponseWriter, ep Endpoint) bool {
	i.mu.Lock()
	i.hits[ep]++
	status := i.status[ep]
	i.mu.Unlock()
	if status == 0 {
		return false
	}
	w.WriteHeader(status)
	return true
}

func (i *IDP) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	if i.failed(w, EndpointDiscovery) {
		return
	}
	doc := map[string]any{
		"issuer":                                i.srv.URL,
		"jwks_uri":                              i.JWKSURL(),
		"authorization_endpoint":                i.srv.URL + "/authorize",
		"token_endpoint":                        i.srv.URL + "/token",
		"response_types_supported":              []string{"code"},
		"id_token_signing_alg_values_supported": []string{string(i.alg)},
		"code_challenge_methods_supported":      []string{"S256"},
	}
	i.mu.Lock()
	for k, v := range i.discovery {
		if v == nil {
			delete(doc, k)
			continue
		}
		doc[k] = v
	}
	i.mu.Unlock()
	writeJSON(w, doc)
}

func (i *IDP) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	if i.failed(w, EndpointJWKS) {
		return
	}
	writeJSON(w, i.jwks)
}

func (i *IDP) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if i.failed(w, EndpointAuthorize) {
		return
	}
	q := r.URL.Query()
	req := AuthorizeRequest{
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		ResponseType:        q.Get("response_type"),
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		Nonce:               q.Get("nonce"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
	}
	if req.RedirectURI == "" || req.ClientID != i.clientID {
		http.Error(w, "invalid authorize request", http.StatusBadRequest)
		return
	}
	code := randomCode()

	i.mu.Lock()
	i.codes[code] = req
	i.last = req
	i.mu.Unlock()

	to, err := url.Parse(req.RedirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	rq := to.Query()
	rq.Set("code", code)
	rq.Set("state", req.State)
	to.RawQuery = rq.Encode()
	http.Redirect(w, r, to.String(), http.StatusFound) //nolint:gosec // редирект на redirect_uri запроса — суть /authorize у stub-провайдера
}

func (i *IDP) handleToken(w http.ResponseWriter, r *http.Request) {
	if i.failed(w, EndpointToken) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	i.mu.Lock()
	body := i.tokenBody
	authz, known := i.codes[r.PostForm.Get("code")]
	delete(i.codes, r.PostForm.Get("code"))
	i.mu.Unlock()

	if body != "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
		return
	}
	// Код выдаётся только через /authorize; тест, дергающий /token напрямую,
	// получает токен без привязки к nonce.
	if known && authz.CodeChallenge != "" && !verifyPKCE(authz.CodeChallenge, r.PostForm.Get("code_verifier")) {
		http.Error(w, "invalid code_verifier", http.StatusBadRequest)
		return
	}

	claims := map[string]any{}
	if authz.Nonce != "" {
		claims["nonce"] = authz.Nonce
	}
	writeJSON(w, map[string]any{
		"id_token":     i.IDToken(claims),
		"access_token": i.IDToken(claims),
		"token_type":   "Bearer",
		"expires_in":   int(i.tokenTTL.Seconds()),
	})
}

func verifyPKCE(challenge, verifier string) bool {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func randomCode() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("oidctest: random: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
