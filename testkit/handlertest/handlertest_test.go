package handlertest_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
)

type createInput struct {
	Body struct {
		Name string `json:"name" minLength:"3"`
	}
}

type emptyOutput struct{}

func newAPI(t *testing.T) handlertest.API {
	t.Helper()
	return handlertest.NewAPI(t, func(api huma.API) {
		huma.Register(api, huma.Operation{
			OperationID: "get-user",
			Method:      http.MethodGet,
			Path:        "/users/{id}",
		}, func(_ context.Context, _ *struct {
			ID string `path:"id"`
		},
		) (*emptyOutput, error) {
			return nil, httpkit.NotFound("USER_NOT_FOUND", "user does not exist")
		})

		huma.Register(api, huma.Operation{
			OperationID: "create-user",
			Method:      http.MethodPost,
			Path:        "/users",
		}, func(_ context.Context, _ *createInput) (*emptyOutput, error) {
			return &emptyOutput{}, nil
		})
	})
}

// Тело httpkit.APIError доходит до теста целиком: code/status/title/detail и
// request_id из X-Request-ID ответа.
func TestDecodeAPIError_Problem(t *testing.T) {
	resp := newAPI(t).Get("/users/42")

	got := handlertest.DecodeAPIError(t, resp)
	if got.Code != "USER_NOT_FOUND" {
		t.Errorf("Code = %q, want USER_NOT_FOUND", got.Code)
	}
	if got.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", got.Status, http.StatusNotFound)
	}
	if got.Detail != "user does not exist" {
		t.Errorf("Detail = %q", got.Detail)
	}
	if got.Title != http.StatusText(http.StatusNotFound) {
		t.Errorf("Title = %q", got.Title)
	}
	if got.RequestID == "" || got.RequestID != resp.Header().Get(httpkit.HeaderRequestID) {
		t.Errorf("RequestID = %q, want X-Request-ID %q", got.RequestID, resp.Header().Get(httpkit.HeaderRequestID))
	}
	if len(got.Errors) != 0 {
		t.Errorf("Errors = %+v, want empty", got.Errors)
	}
}

// Валидация huma — тот же APIError: 422 VALIDATION_FAILED, поля в errors[] с
// кодом INVALID и без эха значения.
func TestDecodeAPIError_HumaValidation(t *testing.T) {
	resp := newAPI(t).Post("/users", map[string]any{"name": "ab"})
	handlertest.AssertAPIError(t, resp, http.StatusUnprocessableEntity, "VALIDATION_FAILED")

	got := handlertest.DecodeAPIError(t, resp)
	if got.Title == "" || got.Detail == "" {
		t.Errorf("top-level fields lost: %+v", got)
	}
	want := httpkit.FieldError{Location: "body.name", Code: "INVALID", Message: "expected length >= 3"}
	if len(got.Errors) != 1 || got.Errors[0] != want {
		t.Fatalf("Errors = %+v, want [%+v]", got.Errors, want)
	}
	if strings.Contains(resp.Body.String(), `"ab"`) {
		t.Errorf("the field value must not be echoed: %s", resp.Body.String())
	}
}

// Неизвестный маршрут и метод — тоже контракт ошибок, как у сервера.
func TestNewAPI_RouterErrors(t *testing.T) {
	api := newAPI(t)
	handlertest.AssertAPIError(t, api.Get("/nowhere"), http.StatusNotFound, "NOT_FOUND")
	resp := api.Delete("/users/42")
	handlertest.AssertAPIError(t, resp, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
	if allow := resp.Header().Values("Allow"); !slices.Equal(allow, []string{http.MethodGet, http.MethodHead}) {
		t.Errorf("Allow = %v, want [GET HEAD]", allow)
	}
}

func TestAssertAPIError(t *testing.T) {
	resp := newAPI(t).Get("/users/42")
	handlertest.AssertAPIError(t, resp, http.StatusNotFound, "USER_NOT_FOUND")
}

func TestAssertStatus_Success(t *testing.T) {
	resp := newAPI(t).Post("/users", map[string]any{"name": "alice"})
	handlertest.AssertStatus(t, resp, http.StatusNoContent)
}

// AssertAPIError держит контракт: ответ не problem+json или без request_id — провал.
func TestAssertAPIError_Contract(t *testing.T) {
	cases := map[string]func(*httptest.ResponseRecorder){
		"не problem+json":  func(r *httptest.ResponseRecorder) { r.Header().Set("Content-Type", "application/json") },
		"чужой request_id": func(r *httptest.ResponseRecorder) { r.Header().Set(httpkit.HeaderRequestID, "other") },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			tb := &recordTB{}
			resp := newAPI(t).Get("/users/42")
			spoil(resp)
			handlertest.AssertAPIError(tb, resp, http.StatusNotFound, "USER_NOT_FOUND")
			if tb.fatals == 0 {
				t.Fatal("AssertAPIError must fail")
			}
		})
	}
}

func TestDecodeAPIError_NonJSONBodyFails(t *testing.T) {
	tb := &recordTB{}
	resp := newAPI(t).Get("/users/42")
	resp.Body.Reset()
	resp.Body.WriteString("not json")

	handlertest.DecodeAPIError(tb, resp)
	if tb.fatals != 1 {
		t.Fatalf("fatals = %d, want 1 on a non-JSON body", tb.fatals)
	}
}

type recordTB struct {
	testing.TB
	fatals int
}

func (tb *recordTB) Helper() {}

func (tb *recordTB) Fatalf(string, ...any) { tb.fatals++ }

func registerSecret(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-secret",
		Method:      http.MethodGet,
		Path:        "/secret",
		Security:    []map[string][]string{{"Bearer": {}}},
	}, func(context.Context, *struct{}) (*emptyOutput, error) {
		return &emptyOutput{}, nil
	})
}

// Auth-гейт — как у сервера: HumaAuth до маршрутов, extractor — у схемы из WithSecuritySchemes.
func TestNewAPI_SchemeExtractor(t *testing.T) {
	api := handlertest.NewAPI(t, registerSecret, handlertest.WithSecuritySchemes(httpkit.SecuritySchemeEntry{
		Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"},
		Extract: func(r *http.Request) (context.Context, error) {
			if r.Header.Get("Authorization") == "" {
				return nil, errors.New("no credentials")
			}
			return r.Context(), nil
		},
	}))

	handlertest.AssertAPIError(t, api.Get("/secret"), http.StatusUnauthorized, "UNAUTHORIZED")
	handlertest.AssertStatus(t, api.Get("/secret", "Authorization: Bearer alice"), http.StatusNoContent)
}

// KTST-08: схема без extractor'а закрывает операцию (500 AUTH_NOT_CONFIGURED), а не открывает, — и
// HumaAuth потребителя после маршрутов её не откроет.
func TestNewAPI_SecuredOperationClosedByDefault(t *testing.T) {
	bearer := httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"}}
	api := handlertest.NewAPI(t, func(api huma.API) {
		registerSecret(api)
		allow := bearer
		allow.Extract = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
		api.UseMiddleware(httpkit.HumaAuth([]httpkit.SecuritySchemeEntry{allow}, logkit.Nop()))
	}, handlertest.WithSecuritySchemes(bearer))
	handlertest.AssertAPIError(t, api.Get("/secret", "Authorization: Bearer alice"), http.StatusInternalServerError, "AUTH_NOT_CONFIGURED")
}

// Маршруты register проходят проверку старта Module: дубль METHOD+path и, при схеме, операция
// без Security и без httpkit.Public — провал теста.
func TestNewAPI_ChecksRoutes(t *testing.T) {
	bearer := httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"}}
	open := func(api huma.API) {
		huma.Register(api, huma.Operation{OperationID: "list", Method: http.MethodGet, Path: "/items"},
			func(context.Context, *struct{}) (*emptyOutput, error) { return &emptyOutput{}, nil })
	}
	cases := map[string]struct {
		register func(huma.API)
		opts     []handlertest.Option
		fatals   int
	}{
		"без схем открытая операция": {register: open},
		"при схеме — без Public":     {register: open, opts: []handlertest.Option{handlertest.WithSecuritySchemes(bearer)}, fatals: 1},
		"при схеме — Security и Public": {register: func(api huma.API) {
			registerSecret(api)
			huma.Register(api, httpkit.Public(huma.Operation{OperationID: "status", Method: http.MethodGet, Path: "/status"}),
				func(context.Context, *struct{}) (*emptyOutput, error) { return &emptyOutput{}, nil })
		}, opts: []handlertest.Option{handlertest.WithSecuritySchemes(bearer)}},
		"дубль METHOD+path": {register: func(api huma.API) {
			open(api)
			huma.Register(api, huma.Operation{OperationID: "list-again", Method: http.MethodGet, Path: "/items"},
				func(context.Context, *struct{}) (*emptyOutput, error) { return &emptyOutput{}, nil })
		}, fatals: 1},
		// Adapter().Handle поверх операции заменил бы её хендлер мимо HumaAuth.
		"Adapter().Handle поверх операции": {register: func(api huma.API) {
			registerSecret(api)
			api.Adapter().Handle(&huma.Operation{Method: http.MethodGet, Path: "/secret"}, func(huma.Context) {})
		}, opts: []handlertest.Option{handlertest.WithSecuritySchemes(bearer)}, fatals: 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tb := &recordTB{}
			handlertest.NewAPI(tb, c.register, c.opts...)
			if tb.fatals != c.fatals {
				t.Fatalf("fatals = %d, want %d", tb.fatals, c.fatals)
			}
		})
	}
}

type putInput struct {
	Body struct {
		Blob string `json:"blob"`
	}
}

func registerPut(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "put-blob", Method: http.MethodPut, Path: "/blob"},
		func(context.Context, *putInput) (*emptyOutput, error) { return &emptyOutput{}, nil })
}

// KTST-09: конфиг — как у сервера: по умолчанию httpkit.NewConfig() (ENV), иначе WithConfig.
func TestNewAPI_ServerConfig(t *testing.T) {
	body := map[string]any{"blob": strings.Repeat("x", 4096)}

	t.Setenv("HTTP_MAX_BODY_BYTES", "1024")
	handlertest.AssertAPIError(t, handlertest.NewAPI(t, registerPut).Put("/blob", body), http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE")

	cfg, err := httpkit.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxBody.MaxBytes = 1 << 20
	handlertest.AssertStatus(t, handlertest.NewAPI(t, registerPut, handlertest.WithConfig(cfg)).Put("/blob", body), http.StatusNoContent)

	tb := &recordTB{}
	handlertest.NewAPI(tb, registerPut, handlertest.WithConfig(httpkit.Config{}))
	if tb.fatals == 0 {
		t.Fatal("an invalid WithConfig must fail the test")
	}
}

// Дефолт docs — как у сервера: карта API выключена, WithConfig её включает.
func TestNewAPI_DocsLikeServer(t *testing.T) {
	t.Setenv("HTTP_DOCS_ENABLED", "")
	if err := os.Unsetenv("HTTP_DOCS_ENABLED"); err != nil {
		t.Fatal(err)
	}
	handlertest.AssertAPIError(t, handlertest.NewAPI(t, registerPut).Get("/openapi.json"), http.StatusNotFound, "NOT_FOUND")

	cfg, err := httpkit.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DocsEnabled = true
	handlertest.AssertStatus(t, handlertest.NewAPI(t, registerPut, handlertest.WithConfig(cfg)).Get("/openapi.json"), http.StatusOK)
}

// Логгер — как у сервера: причина 5xx — строкой handler error с request_id ответа.
func TestNewAPI_WithLogger(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Register(api, huma.Operation{OperationID: "boom", Method: http.MethodGet, Path: "/boom"},
			func(context.Context, *struct{}) (*emptyOutput, error) {
				return nil, errors.New("pq: connection refused")
			})
	}, handlertest.WithLogger(log))

	resp := api.Get("/boom")
	handlertest.AssertAPIError(t, resp, http.StatusInternalServerError, "INTERNAL")
	line := buf.String()
	if !strings.Contains(line, `"msg":"handler error"`) || !strings.Contains(line, "pq: connection refused") ||
		!strings.Contains(line, resp.Header().Get(httpkit.HeaderRequestID)) {
		t.Fatalf("handler error line with request_id: %s", line)
	}
}

// IP из ContextWithClientIP — тот же, что middleware ClientIP кладёт по адресу соединения: тест
// usecase'а видит адрес в канонической форме, как в проде; не IP — ctx без адреса.
func TestContextWithClientIP(t *testing.T) {
	var viaMiddleware string
	h := httpkit.ClientIP(0, logkit.Nop())(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		viaMiddleware = httpkit.ClientIPFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[::ffff:192.0.2.1]:4711"
	h.ServeHTTP(httptest.NewRecorder(), req)

	got := httpkit.ClientIPFrom(handlertest.ContextWithClientIP(context.Background(), "::ffff:192.0.2.1"))
	if got != "192.0.2.1" || got != viaMiddleware {
		t.Fatalf("ClientIPFrom = %q, middleware = %q, want both 192.0.2.1", got, viaMiddleware)
	}
	if got := httpkit.ClientIPFrom(handlertest.ContextWithClientIP(context.Background(), "not-an-ip")); got != "" {
		t.Fatalf("invalid ip: ClientIPFrom = %q, want empty", got)
	}
}
