package oidckit

import (
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
	"git.stit.tech/stit-core/golibinfra/testkit/oidctest"
)

func moduleDeps(t *testing.T, cfg Config, api huma.API) fx.Option {
	t.Helper()
	signer := sessionSigner(t)
	hcCfg, err := httpclient.NewConfig()
	if err != nil {
		t.Fatalf("httpclient config: %v", err)
	}
	client, err := httpclient.New(hcCfg)
	if err != nil {
		t.Fatalf("httpclient: %v", err)
	}
	return fx.Options(
		fx.Supply(cfg, devCookies, client),
		fx.Provide(
			func() huma.API { return api },
			func() authnkit.JWTSigner { return signer },
			func() authnkit.JWTParser { return signer },
			func() StateStore { return NewCacheStateStore(cachekit.NewMemoryCache()) },
			logkit.Nop,
		),
	)
}

// Module собирается целиком: extractor, security-scheme, health-проба и роуты.
func TestModule_Graph(t *testing.T) {
	idp := oidctest.New(t)
	cfg := testConfig(idp)
	cfg.DiscoveryOnStart = false

	var (
		probes  []healthkit.Probe
		schemes []httpkit.SecuritySchemeEntry
	)
	api := handlertest.NewAPI(t, func(huma.API) {})
	app := fxtest.New(t,
		moduleDeps(t, cfg, api),
		Module,
		fx.Invoke(func(in struct {
			fx.In

			Probes  []healthkit.Probe             `group:"healthkit.probes"`
			Schemes []httpkit.SecuritySchemeEntry `group:"httpkit.security_scheme"`
		},
		) {
			probes, schemes = in.Probes, in.Schemes
		}),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)

	if len(probes) != 1 || probes[0].Name != "oidckit" {
		t.Fatalf("health-пробы: %+v", probes)
	}
	if len(schemes) != 1 || schemes[0].Name != SecuritySchemeName || schemes[0].Scheme.Name != CookieAccess || schemes[0].Extract == nil {
		t.Fatalf("security-scheme: %+v", schemes)
	}
	if _, ok := api.OpenAPI().Paths["/auth/login"]; !ok {
		t.Fatalf("роуты не зарегистрированы: %v", api.OpenAPI().Paths)
	}
	// Discovery не выполнялось: OIDC_DISCOVERY_ON_START=false.
	if got := idp.Hits(oidctest.EndpointDiscovery); got != 0 {
		t.Fatalf("discovery на старте: %d", got)
	}
}

// Без *httpclient.Client в графе приложение не собирается: собственного клиента
// kit не заводит.
func TestModule_RequiresHTTPClient(t *testing.T) {
	idp := oidctest.New(t)
	signer := sessionSigner(t)
	api := handlertest.NewAPI(t, func(huma.API) {})

	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Supply(testConfig(idp), devCookies),
		fx.Provide(
			func() huma.API { return api },
			func() authnkit.JWTSigner { return signer },
			func() authnkit.JWTParser { return signer },
			func() StateStore { return NewCacheStateStore(cachekit.NewMemoryCache()) },
			logkit.Nop,
		),
		Module,
	)
	if err == nil {
		t.Fatal("граф без httpclient.Client собрался")
	}
}

// OIDC_DISCOVERY_ON_START=true — недоступный idp роняет старт приложения.
func TestModule_DiscoveryOnStartFailsFast(t *testing.T) {
	idp := oidctest.New(t)
	idp.SetStatus(oidctest.EndpointDiscovery, 500)
	cfg := testConfig(idp)
	cfg.DiscoveryOnStart = true

	api := handlertest.NewAPI(t, func(huma.API) {})
	app := fx.New(fx.NopLogger, moduleDeps(t, cfg, api), Module)
	if err := app.Start(t.Context()); err == nil {
		t.Fatal("приложение стартовало при недоступном discovery")
		_ = app.Stop(t.Context())
	}
}

// Снипет README § Быстрый старт собирается как есть: копипаст из доки не должен
// падать на старте dig-ошибкой о недостающем типе.
func TestModule_READMEQuickStartGraph(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Provide(
			authnkit.NewJWTConfig,
			cachekit.NewConfig,
			httpclient.NewConfig,
			httpkit.NewConfig,
			logkit.NewConfig,
			NewConfig,
			NewCookieConfig,
			NewCacheStateStore,
			func() Hooks { return Hooks{} },
		),
		authnkit.Module,
		cachekit.Module,
		httpclient.Module,
		httpkit.Module,
		logkit.Module,
		Module,
	)
	if err != nil {
		t.Fatalf("граф из README не собирается: %v", err)
	}
}

func TestModule_InvalidConfigFailsGraph(t *testing.T) {
	idp := oidctest.New(t)
	cfg := testConfig(idp)
	cfg.RedirectURL = ""

	api := handlertest.NewAPI(t, func(huma.API) {})
	if err := fx.New(fx.NopLogger, moduleDeps(t, cfg, api), Module).Err(); err == nil {
		t.Fatal("невалидный Config собрался в граф")
	}
}
