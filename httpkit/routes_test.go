package httpkit_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
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
	return httpkit.ProvideSecurityScheme(
		func() httpkit.SecuritySchemeEntry { return httpkit.SecuritySchemeEntry{Name: name, Scheme: scheme} },
	)
}

var (
	bearerScheme = &huma.SecurityScheme{Type: "http", Scheme: "bearer"}
	withBearer   = httpkit.ProvideSecurityScheme(func() httpkit.SecuritySchemeEntry {
		// Субъект — предъявленный токен: личность запроса для идемпотентности.
		return httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: bearerScheme, Extract: func(r *http.Request) (context.Context, error) {
			return httpkit.ContextWithSubject(r.Context(), r.Header.Get("Authorization")), nil
		}}
	})
	bearerOnly = []map[string][]string{{"Bearer": {}}}
)

func noBody(context.Context, *struct{}) (*struct{}, error) { return nil, nil } // тело ответа не нужно

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
// дубль METHOD+path, Public с Security и Security из одних `{}` — ошибка старта и без схем.
func TestModule_RoutesCheck(t *testing.T) {
	rawRoute := func(path string) fx.Option {
		return fx.Invoke(func(srv *httpkit.Server) {
			srv.Router().Get(path, func(http.ResponseWriter, *http.Request) {})
		})
	}
	item := secured(operation("get-item", http.MethodGet, "/items/{id}"))
	adapterRoute := func(api huma.API) {
		api.Adapter().Handle(&huma.Operation{Method: http.MethodGet, Path: "/items/{id}"}, func(huma.Context) {})
	}
	// chi оставляет маршруту последний хендлер: сырой поверх операции открыл бы её анониму.
	overItem := func(first, then func(srv *httpkit.Server, api huma.API)) fx.Option {
		return fx.Invoke(func(srv *httpkit.Server, api huma.API) { first(srv, api); then(srv, api) })
	}
	registerItem := func(_ *httpkit.Server, api huma.API) { huma.Register(api, item, noBody) }
	rawItem := func(pattern string) func(*httpkit.Server, huma.API) {
		return func(srv *httpkit.Server, _ huma.API) {
			srv.Router().Get(pattern, func(http.ResponseWriter, *http.Request) {})
		}
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
		{name: "Security из одних пустых альтернатив — не способ открыть", opts: []fx.Option{withBearer, register(huma.Operation{
			OperationID: "wipe", Method: http.MethodPost, Path: "/wipe", Security: []map[string][]string{{}},
		})}, want: []string{"operation wipe has only empty Security alternatives: mark it httpkit.Public"}},
		{name: "пустые альтернативы и без схем", opts: []fx.Option{register(huma.Operation{
			OperationID: "wipe", Method: http.MethodPost, Path: "/wipe", Security: []map[string][]string{{}, {}},
		})}, want: []string{"operation wipe has only empty Security alternatives"}},
		{name: "необязательная аутентификация", opts: []fx.Option{withBearer, register(huma.Operation{
			OperationID: "feed", Method: http.MethodGet, Path: "/feed", Security: []map[string][]string{{}, {"Bearer": {}}},
		})}},
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
		{
			name: "сырой маршрут поверх операции", opts: []fx.Option{withBearer, overItem(registerItem, rawItem("/items/{id}"))},
			want: []string{"route GET /items/{} replaces operation get-item"},
		},
		{
			name: "сырой маршрут с другим именем параметра", opts: []fx.Option{withBearer, overItem(registerItem, rawItem("/items/{itemID}"))},
			want: []string{"route GET /items/{} replaces operation get-item"},
		},
		{
			name: "Adapter().Handle поверх операции",
			opts: []fx.Option{withBearer, overItem(registerItem, func(_ *httpkit.Server, api huma.API) { adapterRoute(api) })},
			want: []string{"route GET /items/{} replaces operation get-item"},
		},
		{
			name: "сырой маршрут до операции", opts: []fx.Option{withBearer, overItem(rawItem("/items/{id}"), registerItem)},
			want: []string{"route GET /items/{} is registered before operation get-item, which replaces it"},
		},
		{
			name: "Adapter().Handle до операции",
			opts: []fx.Option{withBearer, overItem(func(_ *httpkit.Server, api huma.API) { adapterRoute(api) }, registerItem)},
			want: []string{"route GET /items/{} is registered before operation get-item, which replaces it"},
		},
		{
			name: "сырой маршрут поверх операции и без схем", opts: []fx.Option{fx.Invoke(func(srv *httpkit.Server, api huma.API) {
				huma.Register(api, operation("list", http.MethodGet, "/items"), noBody)
				srv.Router().Get("/items", func(http.ResponseWriter, *http.Request) {})
			})},
			want: []string{"route GET /items replaces operation list"},
		},
		{name: "группа с двумя префиксами — хендлеры своих операций", opts: []fx.Option{withBearer, fx.Invoke(func(api huma.API) {
			huma.Register(huma.NewGroup(api, "/v1", "/v2"), secured(operation("things", http.MethodGet, "/things")), noBody)
		})}},
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
		healthkit.ProvideProbe(probe), healthkit.ProvideProbe(probe)).Err()
	if !errors.Is(err, httpkit.ErrConfig) || !strings.Contains(err.Error(), `readiness probe "cachekit" is provided twice`) {
		t.Fatalf("duplicate probe name must fail the graph: %v", err)
	}
}

// Без fx та же проверка — CheckRoutes; её же зовёт Module на OnStart.
func TestServer_CheckRoutesWithoutFx(t *testing.T) {
	newServer := func(opts ...httpkit.ServerOption) *httpkit.Server {
		cfg := testConfig(t, "127.0.0.1:0")
		return mustServer(t, cfg, append([]httpkit.ServerOption{
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

	if _, err := httpkit.NewServer(testConfig(t, "127.0.0.1:0"), logkit.Nop(), httpkit.WithSecuritySchemes(httpkit.SecuritySchemeEntry{Name: "Broken"})); !errors.Is(err, httpkit.ErrConfig) {
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

// Регрессия VGK-01: группа huma с префиксом отдаёт хендлеру свою копию операции, а хуки OnAddOperation правят копию
// в OpenAPI. Хендлер исполняет ту, что видят OpenAPI и CheckRoutes: Security хука потребителя закрывает операцию
// (401 с вызовом схемы, а не ответ хендлера), тело режет лимит кита — и у RawBody; без группы — так же.
func TestServer_GroupOperationRunsAsDocumented(t *testing.T) {
	jwt := httpkit.SecuritySchemeEntry{Name: "jwt", Scheme: bearerScheme, Extract: func(r *http.Request) (context.Context, error) {
		if r.Header.Get("Authorization") != "Bearer ok" {
			return nil, errors.New("no credentials")
		}
		return httpkit.ContextWithSubject(r.Context(), "u1"), nil
	}}
	secureHooked := httpkit.WithHumaConfig(func(c *huma.Config) {
		c.OnAddOperation = append(c.OnAddOperation, func(_ *huma.OpenAPI, op *huma.Operation) {
			if strings.HasSuffix(op.Path, "/hooked") {
				op.Security = []map[string][]string{{"jwt": {}}}
			}
		})
	})
	cfg := testConfig(t, "127.0.0.1:0")
	cfg.MaxBody.MaxBytes = 64
	srv := mustServer(t, cfg, httpkit.WithSecuritySchemes(jwt), secureHooked)
	api := srv.BindAPI()
	type named struct {
		Body struct {
			Name string `json:"name"`
		}
	}
	jwtOnly := []map[string][]string{{"jwt": {}}}
	apis := []struct {
		id, prefix string
		api        huma.API
	}{{"root", "", api}, {"v1", "/v1", huma.NewGroup(api, "/v1")}}
	for _, a := range apis {
		huma.Register(a.api, huma.Operation{OperationID: a.id + "-hooked", Method: http.MethodGet, Path: "/hooked"}, noBody)
		huma.Register(a.api, huma.Operation{OperationID: a.id + "-raw", Method: http.MethodPost, Path: "/raw", Security: jwtOnly},
			func(context.Context, *struct{ RawBody []byte }) (*struct{}, error) { return nil, nil })
		huma.Register(a.api, huma.Operation{OperationID: a.id + "-upload", Method: http.MethodPost, Path: "/upload", Security: jwtOnly},
			func(context.Context, *named) (*struct{}, error) { return nil, nil })
	}
	if err := srv.CheckRoutes(); err != nil {
		t.Fatalf("CheckRoutes: %v", err)
	}
	serve := func(method, path, body string, headers ...string) *httptest.ResponseRecorder {
		return serveRouter(srv, method, path, body, headers...)
	}
	const auth, asJSON = "Authorization: Bearer ok", "Content-Type: application/json"
	over := `{"name":"` + strings.Repeat("x", 100) + `"}`
	for _, a := range apis {
		t.Run(a.id+" Security of the hook", func(t *testing.T) {
			resp := serve(http.MethodGet, a.prefix+"/hooked", "")
			handlertest.AssertAPIError(t, resp, http.StatusUnauthorized, "UNAUTHORIZED")
			if got := resp.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want the challenge of the scheme Bearer", got)
			}
			handlertest.AssertStatus(t, serve(http.MethodGet, a.prefix+"/hooked", "", auth), http.StatusNoContent)
		})
		t.Run(a.id+" RawBody over the limit", func(t *testing.T) {
			handlertest.AssertAPIError(t, serve(http.MethodPost, a.prefix+"/raw", over, auth), http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE")
		})
		t.Run(a.id+" Body over the limit", func(t *testing.T) {
			handlertest.AssertAPIError(t, serve(http.MethodPost, a.prefix+"/upload", over, auth, asJSON), http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE")
			handlertest.AssertStatus(t, serve(http.MethodPost, a.prefix+"/upload", `{"name":"x"}`, auth, asJSON), http.StatusNoContent)
		})
	}
}

// Урезанный лимит тела операции группы huma с префиксом виден на старте: тело копии группы huma читает по её литералу
// (у Body без него — 1 MiB), и лимит кита или хука выше этого — одна строка WARN на операцию: лимит, которым режет huma,
// запрошенный лимит и подсказка MaxBodyBytes: -1. Без группы, с -1 в литерале, у RawBody и multipart-формы её нет.
func TestServer_GroupBodyLimitCapIsWarned(t *testing.T) {
	const capped = "huma group operation body limit is capped by its literal: MaxBodyBytes: -1 in the literal lifts it"
	type named struct {
		Body struct {
			Name string `json:"name"`
		}
	}
	type form struct {
		RawBody huma.MultipartFormFiles[struct {
			File huma.FormFile `form:"file"`
		}]
	}
	noOutput := func(context.Context, *named) (*struct{}, error) { return nil, nil }
	raise := httpkit.WithHumaConfig(func(c *huma.Config) {
		c.OnAddOperation = append(c.OnAddOperation, func(_ *huma.OpenAPI, op *huma.Operation) {
			if strings.HasSuffix(op.Path, "/raised") {
				op.MaxBodyBytes = 4 << 20
			}
		})
	})
	for _, tc := range []struct {
		name     string
		maxBytes int64
		want     map[string][2]string // операция → поля limit и requested её строки WARN
	}{
		{"server limit off", 0, map[string][2]string{
			"g-upload": {"1048576", "0"}, "g-raised": {"1048576", "4194304"}, "g-small": {"16", "0"},
		}},
		{"default server limit", 1 << 20, map[string][2]string{
			"g-raised": {"1048576", "4194304"}, "g-small": {"16", "1048576"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "127.0.0.1:0")
			cfg.MaxBody.MaxBytes = tc.maxBytes
			log := logtest.Capture()
			srv, err := httpkit.NewServer(cfg, log, raise)
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			api := srv.BindAPI()
			for _, a := range []struct {
				id  string
				api huma.API
			}{{"", api}, {"g-", huma.NewGroup(api, "/v1")}} {
				for _, op := range []huma.Operation{
					{OperationID: a.id + "upload", Method: http.MethodPost, Path: "/upload"},
					{OperationID: a.id + "raised", Method: http.MethodPost, Path: "/raised"},
					{OperationID: a.id + "small", Method: http.MethodPost, Path: "/small", MaxBodyBytes: 16},
					{OperationID: a.id + "big", Method: http.MethodPost, Path: "/big", MaxBodyBytes: -1},
				} {
					huma.Register(a.api, httpkit.Public(op), noOutput)
				}
				huma.Register(a.api, httpkit.Public(huma.Operation{OperationID: a.id + "raw", Method: http.MethodPost, Path: "/raw"}),
					func(context.Context, *struct{ RawBody []byte }) (*struct{}, error) { return nil, nil })
				huma.Register(a.api, httpkit.Public(huma.Operation{OperationID: a.id + "form", Method: http.MethodPost, Path: "/form", MaxBodyBytes: 64}),
					func(context.Context, *form) (*struct{}, error) { return nil, nil })
			}
			got := map[string][2]string{}
			for _, e := range log.Entries() {
				if e.Msg != capped {
					continue
				}
				id := e.Str("operation")
				if _, twice := got[id]; twice || e.Level != slog.LevelWarn {
					t.Errorf("operation %s: a second record or not WARN (%v)", id, e.Level)
				}
				got[id] = [2]string{e.Str("limit"), e.Str("requested")}
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("WARN %q: got %v, want %v", capped, got, tc.want)
			}
		})
	}
}

// serveRouter отвечает на запрос роутером сервера — цепочкой кита целиком, без listener'а; заголовки — «Имя: значение».
func serveRouter(srv *httpkit.Server, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ": ")
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}
