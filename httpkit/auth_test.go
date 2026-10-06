package httpkit_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
)

type principalKey struct{}

type authOutput struct {
	Body struct {
		Principal string `json:"principal"`
	}
}

// bearerEntry — схема Bearer операций тестов auth с extractor'ом extract.
func bearerEntry(extract httpkit.AuthExtractor) httpkit.SecuritySchemeEntry {
	return httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: bearerScheme, Extract: extract}
}

// authAPIs — prod-адаптер (chi) и тестовый (humatest, на котором строится
// handlertest): HumaAuth обязан работать на обоих.
func authAPIs(t *testing.T, log logkit.Logger, extract httpkit.AuthExtractor) map[string]humatest.TestAPI {
	t.Helper()
	chiAPI := humatest.Wrap(t, humachi.New(chi.NewRouter(), huma.DefaultConfig("t", "0")))
	_, flowAPI := humatest.New(t, huma.DefaultConfig("t", "0"))

	out := map[string]humatest.TestAPI{"humachi": chiAPI, "humatest": flowAPI}
	for _, api := range out {
		api.UseMiddleware(httpkit.HumaAuth([]httpkit.SecuritySchemeEntry{bearerEntry(extract)}, log))
		registerAuthRoutes(api)
	}
	return out
}

func registerAuthRoutes(api huma.API) {
	handler := func(ctx context.Context, _ *struct{}) (*authOutput, error) {
		out := &authOutput{}
		out.Body.Principal, _ = ctx.Value(principalKey{}).(string)
		return out, nil
	}
	huma.Register(api, huma.Operation{
		OperationID: "guarded", Method: http.MethodGet, Path: "/guarded",
		Security: []map[string][]string{{"Bearer": {}}},
	}, handler)
	huma.Register(api, httpkit.Public(huma.Operation{
		OperationID: "open", Method: http.MethodGet, Path: "/open",
	}), handler)
}

func TestHumaAuth_RejectsWithCodeAndLogsReason(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "debug"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	extract := func(*http.Request) (context.Context, error) {
		return nil, errors.New("token signature mismatch")
	}

	for name, api := range authAPIs(t, log, extract) {
		t.Run(name, func(t *testing.T) {
			resp := api.Get("/guarded")
			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.Code)
			}
			if ct := resp.Header().Get("Content-Type"); ct != httpkit.ProblemContentType {
				t.Errorf("content-type = %q", ct)
			}
			handlertest.AssertAPIError(t, resp, http.StatusUnauthorized, "UNAUTHORIZED")
			if strings.Contains(resp.Body.String(), "signature mismatch") {
				t.Errorf("reason must not leak to the client: %s", resp.Body.String())
			}
		})
	}
	if !strings.Contains(buf.String(), "auth rejected") || !strings.Contains(buf.String(), "signature mismatch") {
		t.Fatalf("extractor error must be logged: %s", buf.String())
	}
}

// Регрессия: недоступный JWKS/хаб — это 503, а не 401; иначе клиент
// разлогинивает пользователя из-за деградации зависимости.
func TestHumaAuth_UnavailableBackendIs503(t *testing.T) {
	extract := func(*http.Request) (context.Context, error) {
		return nil, fmt.Errorf("jwks fetch failed: %w", httpkit.ErrAuthUnavailable)
	}
	for name, api := range authAPIs(t, logkit.Nop(), extract) {
		t.Run(name, func(t *testing.T) {
			resp := api.Get("/guarded")
			handlertest.AssertAPIError(t, resp, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE")
		})
	}
}

func TestHumaAuth_ExtractorAPIErrorKeepsStatus(t *testing.T) {
	extract := func(*http.Request) (context.Context, error) {
		return nil, httpkit.Forbidden("SESSION_EXPIRED", "session expired")
	}
	for name, api := range authAPIs(t, logkit.Nop(), extract) {
		t.Run(name, func(t *testing.T) {
			handlertest.AssertAPIError(t, api.Get("/guarded"), http.StatusForbidden, "SESSION_EXPIRED")
		})
	}
}

// N8 валидатора: 5xx APIError от extractor'а — строка Error с кодом, а не Debug.
func TestHumaAuth_ExtractorServerErrorIsLogged(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	extract := func(*http.Request) (context.Context, error) {
		return nil, httpkit.Internal("SESSION_STORE_DOWN", "")
	}
	for name, api := range authAPIs(t, log, extract) {
		t.Run(name, func(t *testing.T) {
			handlertest.AssertAPIError(t, api.Get("/guarded"), http.StatusInternalServerError, "SESSION_STORE_DOWN")
		})
	}
	if out := buf.String(); !strings.Contains(out, `"msg":"auth failed"`) || !strings.Contains(out, `"code":"SESSION_STORE_DOWN"`) {
		t.Fatalf("a 5xx from the extractor must be logged: %s", out)
	}
}

func TestHumaAuth_PropagatesContextAndSeesHeaders(t *testing.T) {
	extract := func(r *http.Request) (context.Context, error) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			return nil, errors.New("no token")
		}
		return context.WithValue(r.Context(), principalKey{}, token), nil
	}
	for name, api := range authAPIs(t, logkit.Nop(), extract) {
		t.Run(name, func(t *testing.T) {
			resp := api.Get("/guarded", "Authorization: Bearer alice")
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", resp.Code, resp.Body.String())
			}
			if !strings.Contains(resp.Body.String(), `"principal":"alice"`) {
				t.Fatalf("handler must see the extractor context: %s", resp.Body.String())
			}
		})
	}
}

// Регрессия: операция без Security остаётся публичной — гейт opt-in по
// Operation.Security, и ошибка extractor'а её не закрывает.
func TestHumaAuth_OperationWithoutSecurityStaysOpen(t *testing.T) {
	extract := func(*http.Request) (context.Context, error) { return nil, errors.New("nope") }
	for name, api := range authAPIs(t, logkit.Nop(), extract) {
		t.Run(name, func(t *testing.T) {
			if resp := api.Get("/open"); resp.Code != http.StatusOK {
				t.Fatalf("public operation must not be gated: %d", resp.Code)
			}
		})
	}
}

// Регрессия (FX-03): без extractor'а защищённая операция закрыта, а не публична.
func TestHumaAuth_NilExtractorFailsClosed(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	for name, api := range authAPIs(t, log, nil) {
		t.Run(name, func(t *testing.T) {
			handlertest.AssertAPIError(t, api.Get("/guarded"), http.StatusInternalServerError, "AUTH_NOT_CONFIGURED")
			if resp := api.Get("/open"); resp.Code != http.StatusOK {
				t.Fatalf("operation without Security stays open: %d", resp.Code)
			}
		})
	}
	if !strings.Contains(buf.String(), "auth not configured") || !strings.Contains(buf.String(), `"operation":"guarded"`) {
		t.Fatalf("a secured operation without an extractor must be logged: %s", buf.String())
	}
}

// Схема операции без extractor'а — ошибка старта с именами операций и схемы.
func TestModule_AuthRequiresExtractor(t *testing.T) {
	secured := func(api huma.API) {
		registerAuthRoutes(api)
		huma.Register(api, huma.Operation{
			OperationID: "guarded-too", Method: http.MethodPost, Path: "/guarded",
			Security: []map[string][]string{{"Bearer": {}}},
		}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil }) //nolint:nilnil // тело ответа не нужно
	}

	noExtract := httpkit.ProvideSecurityScheme(func() httpkit.SecuritySchemeEntry { return bearerEntry(nil) })
	app := fx.New(fx.NopLogger, fx.Supply(testConfig(t, "127.0.0.1:0")), fx.Provide(logkit.Nop),
		fx.Invoke(secured), noExtract, httpkit.Module)
	err := app.Start(t.Context())
	if err == nil {
		_ = app.Stop(t.Context())
		t.Fatal("a secured operation whose scheme has no extractor must fail the start")
	}
	if !errors.Is(err, httpkit.ErrAuthNotConfigured) {
		t.Fatalf("want ErrAuthNotConfigured, got %v", err)
	}
	for _, op := range []string{"guarded", "guarded-too"} {
		if !strings.Contains(err.Error(), op) {
			t.Errorf("start error must name operation %s: %v", op, err)
		}
	}
	for _, op := range []string{"open", "get-health", "get-ready"} {
		if strings.Contains(err.Error(), op+",") || strings.HasSuffix(err.Error(), op) {
			t.Errorf("start error must not name public operation %s: %v", op, err)
		}
	}

	extract := func(*http.Request) (context.Context, error) { return nil, errors.New("no credentials") }
	srv := startApp(t, fx.Invoke(secured), httpkit.ProvideSecurityScheme(func() httpkit.SecuritySchemeEntry { return bearerEntry(extract) }))
	if resp := get(t, srv, "/guarded"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("with an extractor the graph starts and gates the operation: %d", resp.StatusCode)
	}
}

// Скрытую операцию OpenAPI не видит, а старт видит: без extractor'а она — тоже ошибка старта.
func TestModule_AuthHiddenOperationNeedsExtractor(t *testing.T) {
	err := startErr(t, httpkit.ProvideSecurityScheme(func() httpkit.SecuritySchemeEntry { return bearerEntry(nil) }), fx.Invoke(func(api huma.API) {
		huma.Register(api, huma.Operation{
			OperationID: "hidden", Method: http.MethodGet, Path: "/hidden", Hidden: true,
			Security: []map[string][]string{{"Bearer": {}}},
		}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil }) //nolint:nilnil // тело ответа не нужно
	}))
	if !errors.Is(err, httpkit.ErrAuthNotConfigured) || !strings.Contains(err.Error(), "hidden") {
		t.Fatalf("hidden secured operation without an extractor must fail the start: %v", err)
	}
}
