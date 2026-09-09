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

// authAPIs — prod-адаптер (chi) и тестовый (humatest, на котором строится
// handlertest): HumaAuth обязан работать на обоих.
func authAPIs(t *testing.T, log logkit.Logger, extract httpkit.AuthExtractor) map[string]humatest.TestAPI {
	t.Helper()
	chiAPI := humatest.Wrap(t, humachi.New(chi.NewRouter(), huma.DefaultConfig("t", "0")))
	_, flowAPI := humatest.New(t, huma.DefaultConfig("t", "0"))

	out := map[string]humatest.TestAPI{"humachi": chiAPI, "humatest": flowAPI}
	for _, api := range out {
		api.UseMiddleware(httpkit.HumaAuth(log, extract))
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
	huma.Register(api, huma.Operation{
		OperationID: "open", Method: http.MethodGet, Path: "/open",
	}, handler)
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

func TestHumaAuth_NilExtractorIsNoop(t *testing.T) {
	for name, api := range authAPIs(t, logkit.Nop(), nil) {
		t.Run(name, func(t *testing.T) {
			if resp := api.Get("/guarded"); resp.Code != http.StatusOK {
				t.Fatalf("without an extractor auth is a no-op: %d", resp.Code)
			}
		})
	}
}
