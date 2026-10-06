package issuer_test

import (
	"crypto/elliptic"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	jose "github.com/go-jose/go-jose/v4"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/issuer"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
)

func TestJWKSHandler_CacheControlAndKeys(t *testing.T) {
	previous := genKey(t, elliptic.P256())
	s := newSigner(t, issuer.KeyConfig{
		ActivePEM:   genPEM(t),
		PreviousPEM: pubPEM(t, previous),
		Issuer:      testIssuer,
		Audience:    testAudience,
		JWKSMaxAge:  90 * time.Second,
	})

	rec := httptest.NewRecorder()
	issuer.JWKSHandler(s)(rec, httptest.NewRequest(http.MethodGet, issuer.JWKSPath, nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=90" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(rec.Body).Decode(&set); err != nil {
		t.Fatalf("decode jwks: %v", err)
	}
	if len(set.Keys) != 2 {
		t.Fatalf("keys = %d, want active + previous", len(set.Keys))
	}
}

func TestRegisterJWKS_HumaRoute(t *testing.T) {
	s := newSigner(t, keyConfig(genPEM(t)))
	api := handlertest.NewAPI(t, func(api huma.API) { issuer.RegisterJWKS(api, s) })

	resp := api.Get(issuer.JWKSPath)
	handlertest.AssertStatus(t, resp, http.StatusOK)
	if got := resp.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		t.Fatalf("decode jwks: %v", err)
	}
	if len(set.Keys) != 1 || set.Keys[0].KeyID != s.ActiveKID() {
		t.Fatalf("keys = %+v", set.Keys)
	}
}

// JWKS публичен (httpkit.Public): сервер со схемой безопасности проходит проверку старта.
func TestRegisterJWKS_PassesServerRouteCheck(t *testing.T) {
	cfg, err := httpkit.NewConfig()
	if err != nil {
		t.Fatalf("httpkit config: %v", err)
	}
	srv, err := httpkit.NewServer(cfg, httpkit.WithSecuritySchemes(httpkit.SecuritySchemeEntry{
		Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"},
	}))
	if err != nil {
		t.Fatalf("httpkit server: %v", err)
	}
	issuer.RegisterJWKS(srv.API(), newSigner(t, keyConfig(genPEM(t))))
	if err := srv.CheckRoutes(); err != nil {
		t.Fatalf("JWKS route fails the httpkit start check: %v", err)
	}
}

func TestModule_Graph(t *testing.T) {
	var (
		signer  *issuer.Signer
		refresh *issuer.RefreshManager
	)
	// huma.API в графе нет: Module обязан собраться и без него (сервис вешает
	// JWKSHandler сам).
	app := fxtest.New(
		t,
		fx.Supply(keyConfig(genPEM(t)), refreshConfig()),
		fx.Provide(func() cachekit.Cache { return cachekit.NewMemoryCache() }),
		issuer.Module,
		fx.Populate(&signer, &refresh),
	)
	app.RequireStart().RequireStop()
	if signer == nil || refresh == nil {
		t.Fatal("Module did not provide the issuer primitives")
	}
}

func TestModule_GraphWithHumaAPI(t *testing.T) {
	var registered bool
	api := handlertest.NewAPI(t, func(huma.API) {})
	app := fxtest.New(
		t,
		fx.Supply(keyConfig(genPEM(t)), refreshConfig()),
		fx.Provide(func() cachekit.Cache { return cachekit.NewMemoryCache() }),
		fx.Provide(func() huma.API { return api }),
		issuer.Module,
		fx.Invoke(func(huma.API) { registered = true }),
	)
	app.RequireStart().RequireStop()
	if !registered {
		t.Fatal("huma.API not consumed")
	}
	handlertest.AssertStatus(t, api.Get(issuer.JWKSPath), http.StatusOK)
}
