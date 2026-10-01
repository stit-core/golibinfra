package httpkit_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// startErr собирает граф с Module и возвращает ошибку сборки или старта; стартовавший граф
// сразу останавливается.
func startErr(t *testing.T, opts ...fx.Option) error {
	t.Helper()
	app := fx.New(append([]fx.Option{
		fx.NopLogger,
		fx.Supply(testConfig(t, "127.0.0.1:0")),
		fx.Provide(logkit.Nop),
		httpkit.Module,
	}, opts...)...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := app.Start(ctx)
	if err == nil {
		_ = app.Stop(ctx)
	}
	return err
}

func schemeEntry(name string, scheme *huma.SecurityScheme) fx.Option {
	return fx.Provide(fx.Annotate(
		func() httpkit.SecuritySchemeEntry { return httpkit.SecuritySchemeEntry{Name: name, Scheme: scheme} },
		fx.ResultTags(`group:"`+httpkit.SecuritySchemeGroup+`"`),
	))
}

var (
	bearerScheme = &huma.SecurityScheme{Type: "http", Scheme: "bearer"}
	withBearer   = fx.Options(
		schemeEntry("Bearer", bearerScheme),
		fx.Supply(httpkit.AuthExtractor(func(r *http.Request) (context.Context, error) { return r.Context(), nil })),
	)
	bearerOnly = []map[string][]string{{"Bearer": {}}}
)

func noBody(context.Context, *struct{}) (*struct{}, error) { return nil, nil } //nolint:nilnil // тело ответа не нужно

func register(ops ...huma.Operation) fx.Option {
	return fx.Invoke(func(api huma.API) {
		for _, op := range ops {
			huma.Register(api, op, noBody)
		}
	})
}

func operation(id, method, path string) huma.Operation {
	return huma.Operation{OperationID: id, Method: method, Path: path}
}

func secured(op huma.Operation) huma.Operation {
	op.Security = bearerOnly
	return op
}

func hidden(op huma.Operation) huma.Operation {
	op.Hidden = true
	return op
}

// TestModule_RoutesCheck: при схеме в графе публичность операции явная (Security или
// Public, в том числе у скрытой и групповой), маршрут мимо huma.Register — в PublicRoutes;
// дубль METHOD+path и Public с Security — ошибка старта и без схем.
func TestModule_RoutesCheck(t *testing.T) {
	rawRoute := func(path string) fx.Option {
		return fx.Invoke(func(srv *httpkit.Server) {
			srv.Router().Get(path, func(http.ResponseWriter, *http.Request) {})
		})
	}
	cases := []struct {
		name string
		opts []fx.Option
		want []string // фрагменты ошибки старта; пусто — граф стартует
	}{
		{name: "без схем операция без Security открыта", opts: []fx.Option{register(operation("list", http.MethodGet, "/items"))}},
		{
			name: "операция без Security и без Public", opts: []fx.Option{withBearer, register(operation("list", http.MethodGet, "/items"))},
			want: []string{"operation list has no Security and is not httpkit.Public"},
		},
		{
			name: "скрытая операция", opts: []fx.Option{withBearer, register(hidden(operation("internal", http.MethodPost, "/internal")))},
			want: []string{"operation internal has no Security"},
		},
		{name: "операция группы huma", opts: []fx.Option{withBearer, fx.Invoke(func(api huma.API) {
			huma.Register(huma.NewGroup(api, "/v1"), operation("grouped", http.MethodGet, "/things"), noBody)
		})}, want: []string{"operation grouped has no Security"}},
		{name: "пустой Security не публичен", opts: []fx.Option{withBearer, register(huma.Operation{
			OperationID: "empty", Method: http.MethodGet, Path: "/empty", Security: []map[string][]string{},
		})}, want: []string{"operation empty has no Security"}},
		{name: "Public и Security у операции открыты явно", opts: []fx.Option{withBearer, register(
			httpkit.Public(operation("status", http.MethodGet, "/status")),
			httpkit.Public(hidden(operation("probe", http.MethodGet, "/probe"))),
			secured(operation("me", http.MethodGet, "/me")),
		)}},
		{
			name: "Public с Security", opts: []fx.Option{withBearer, register(httpkit.Public(secured(operation("both", http.MethodGet, "/both"))))},
			want: []string{"operation both is httpkit.Public but has Security"},
		},
		{name: "дубль METHOD+path с другим именем параметра", opts: []fx.Option{register(
			operation("first", http.MethodGet, "/items/{id}"), operation("second", http.MethodGet, "/items/{itemID}"),
		)}, want: []string{"operations first and second share GET /items/{}"}},
		{name: "схема Security вне components", opts: []fx.Option{withBearer, register(huma.Operation{
			OperationID: "admin", Method: http.MethodDelete, Path: "/admin", Security: []map[string][]string{{"AdminKey": {}}},
		})}, want: []string{`operation admin requires security scheme "AdminKey" missing from components`}},
		{
			name: "сырой маршрут chi", opts: []fx.Option{withBearer, rawRoute("/raw")},
			want: []string{"route GET /raw bypasses huma.Register"},
		},
		{name: "Adapter().Handle мимо huma.Register", opts: []fx.Option{withBearer, fx.Invoke(func(api huma.API) {
			api.Adapter().Handle(&huma.Operation{Method: http.MethodPost, Path: "/adapter", Security: bearerOnly}, func(huma.Context) {})
		})}, want: []string{"route POST /adapter bypasses huma.Register"}},
		{name: "сырой маршрут в PublicRoutes", opts: []fx.Option{withBearer, rawRoute("/files/{name}"), fx.Supply(httpkit.PublicRoutes{"/files/{file}"})}},
		{
			name: "PublicRoutes без маршрута", opts: []fx.Option{withBearer, fx.Supply(httpkit.PublicRoutes{"/gone"})},
			want: []string{"httpkit.PublicRoutes lists /gone, but no route bypasses huma.Register there"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HTTP_DOCS_ENABLED", "true") // маршруты документации — свои у кита, не сырые
			err := startErr(t, c.opts...)
			if len(c.want) == 0 {
				if err != nil {
					t.Fatalf("graph must start: %v", err)
				}
				return
			}
			if !errors.Is(err, httpkit.ErrRoutes) {
				t.Fatalf("want ErrRoutes, got %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("start error must contain %q: %v", w, err)
				}
			}
		})
	}
}

// Значения группы схем проверяются при сборке графа: из двух одноимённых в OpenAPI попала бы
// случайная, nil-схема делает документ невалидным.
func TestModule_SecuritySchemeValues(t *testing.T) {
	cookie := func(name string) *huma.SecurityScheme {
		return &huma.SecurityScheme{Type: "apiKey", In: "cookie", Name: name}
	}
	for name, c := range map[string]struct {
		opts []fx.Option
		want []string
	}{
		"дубль имени": {
			opts: []fx.Option{schemeEntry("Cookie", cookie("oidc_session")), schemeEntry("Cookie", cookie("hub_session"))},
			want: []string{`security scheme "Cookie" is provided twice`, "oidc_session", "hub_session"},
		},
		"схема без Scheme": {opts: []fx.Option{schemeEntry("Broken", nil)}, want: []string{`security scheme "Broken" needs a Name and a Scheme`}},
		"схема без имени":  {opts: []fx.Option{schemeEntry("", bearerScheme)}, want: []string{`security scheme "" needs a Name and a Scheme`}},
	} {
		t.Run(name, func(t *testing.T) {
			err := fx.New(append([]fx.Option{fx.NopLogger, fx.Supply(testConfig(t, "127.0.0.1:0")), fx.Provide(logkit.Nop), httpkit.Module}, c.opts...)...).Err()
			if !errors.Is(err, httpkit.ErrConfig) {
				t.Fatalf("want ErrConfig from fx.New, got %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error must contain %q: %v", w, err)
				}
			}
		})
	}
}

// Две пробы с одним именем /ready гонял бы обе и не различал в логе — ошибка сборки графа.
func TestModule_DuplicateProbeName(t *testing.T) {
	probe := func() healthkit.Probe { return healthkit.New("cachekit", func(context.Context) error { return nil }) }
	err := fx.New(fx.NopLogger, fx.Supply(testConfig(t, "127.0.0.1:0")), fx.Provide(logkit.Nop), httpkit.Module,
		healthkit.Provide(probe), healthkit.Provide(probe)).Err()
	if !errors.Is(err, httpkit.ErrConfig) || !strings.Contains(err.Error(), `readiness probe "cachekit" is provided twice`) {
		t.Fatalf("duplicate probe name must fail the graph: %v", err)
	}
}

// Без fx та же проверка — CheckRoutes; её же зовёт Module на OnStart.
func TestServer_CheckRoutesWithoutFx(t *testing.T) {
	newServer := func(opts ...httpkit.ServerOption) *httpkit.Server {
		cfg := testConfig(t, "127.0.0.1:0")
		return httpkit.NewServer(cfg, append([]httpkit.ServerOption{
			httpkit.WithSecuritySchemes(httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: bearerScheme}),
		}, opts...)...)
	}
	s := newServer()
	s.Router().Get("/metrics", func(http.ResponseWriter, *http.Request) {}) // до BindAPI — тоже сырой маршрут
	huma.Register(s.API(), httpkit.Public(operation("status", http.MethodGet, "/status")), noBody)
	if err := s.CheckRoutes(); !errors.Is(err, httpkit.ErrRoutes) || !strings.Contains(err.Error(), "route GET /metrics") {
		t.Fatalf("raw route without PublicRoutes: %v", err)
	}

	s = newServer(httpkit.WithPublicRoutes("/metrics"))
	s.Router().Get("/metrics", func(http.ResponseWriter, *http.Request) {})
	huma.Register(s.API(), httpkit.Public(operation("status", http.MethodGet, "/status")), noBody)
	if err := s.CheckRoutes(); err != nil {
		t.Fatalf("declared raw route and public operation: %v", err)
	}

	broken := httpkit.NewServer(testConfig(t, "127.0.0.1:0"), httpkit.WithSecuritySchemes(httpkit.SecuritySchemeEntry{Name: "Broken"}))
	if err := broken.CheckRoutes(); !errors.Is(err, httpkit.ErrConfig) {
		t.Fatalf("nil scheme: %v", err)
	}
}

func TestPublic_KeepsCallerMetadata(t *testing.T) {
	base := huma.Operation{Metadata: map[string]any{"k": "v"}}
	public := httpkit.Public(base)
	if len(base.Metadata) != 1 || len(public.Metadata) != 2 || public.Metadata["k"] != "v" {
		t.Fatalf("Public must copy metadata: base %v, public %v", base.Metadata, public.Metadata)
	}
}
