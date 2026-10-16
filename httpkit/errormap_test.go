package httpkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
)

var (
	errNotFound = errors.New("user not found")
	errConflict = errors.New("concurrent update")
	errWeak     = errors.New("weak password")
	errLocked   = errors.New("account locked")
	// errRateLimited и errTooLarge — отказы своих middleware потребителя: правило лишь объявляет код.
	errRateLimited = errors.New("rate limited")
	errTooLarge    = errors.New("payload too large")
)

type forbiddenError struct{ action string }

func (e *forbiddenError) Error() string { return "forbidden: " + e.action }

// lockedError — блокировка входа: знает, когда повторить попытку.
type lockedError struct{ wait time.Duration }

func (e *lockedError) Error() string             { return "locked" }
func (e *lockedError) Unwrap() error             { return errLocked }
func (e *lockedError) RetryAfter() time.Duration { return e.wait }

func isForbidden(err error) bool {
	_, ok := errors.AsType[*forbiddenError](err)
	return ok
}

var userErrors = httpkit.NewErrorMap(
	httpkit.ErrorRule{Err: errNotFound, Status: http.StatusNotFound, Code: "USER_NOT_FOUND", Detail: "user not found"},
	httpkit.ErrorRule{Err: errConflict, Status: http.StatusConflict, Code: "USER_CONCURRENT_UPDATE", Detail: "user changed concurrently"},
	httpkit.ErrorRule{Match: isForbidden, Status: http.StatusForbidden, Code: "FORBIDDEN", Detail: "forbidden"},
	httpkit.ErrorRule{Err: errWeak, Status: http.StatusUnprocessableEntity, Code: "USER_WEAK_PASSWORD", Detail: "password too weak", Field: "body.password"},
	httpkit.ErrorRule{Err: errLocked, Status: http.StatusTooManyRequests, Code: "AUTH_LOCKED", Detail: "too many attempts"},
)

func TestErrorMap_Map(t *testing.T) {
	if userErrors.Map(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("repo: %w", errNotFound), http.StatusNotFound, "USER_NOT_FOUND"},
		{fmt.Errorf("usecase: %w", &forbiddenError{action: "delete"}), http.StatusForbidden, "FORBIDDEN"},
		{errConflict, http.StatusConflict, "USER_CONCURRENT_UPDATE"},
	}
	for _, c := range cases {
		var apiErr *httpkit.APIError
		if !errors.As(userErrors.Map(c.err), &apiErr) || apiErr.Status != c.status || apiErr.Code != c.code {
			t.Errorf("Map(%v) = %#v, want %d %s", c.err, apiErr, c.status, c.code)
		}
		if apiErr != nil && (apiErr.Title != http.StatusText(c.status) || len(apiErr.Errors) != 0) {
			t.Errorf("Map(%v): title %q, errors %+v", c.err, apiErr.Title, apiErr.Errors)
		}
	}

	var apiErr *httpkit.APIError
	if !errors.As(userErrors.Map(errWeak), &apiErr) {
		t.Fatal("weak password must map")
	}
	want := httpkit.FieldError{Location: "body.password", Code: "USER_WEAK_PASSWORD", Message: "password too weak"}
	if apiErr.Status != http.StatusUnprocessableEntity || len(apiErr.Errors) != 1 || apiErr.Errors[0] != want {
		t.Fatalf("rule with Field must list the field: %#v", apiErr)
	}

	// Регрессия: посторонняя ошибка (упавшая БД) не превращается в доменный статус.
	dbDown := errors.New("connection refused")
	if got := userErrors.Map(dbDown); !errors.Is(got, dbDown) || errors.As(got, &apiErr) {
		t.Fatalf("unmapped error must be returned as is, got %v", got)
	}
}

func TestErrorMap_Unmapped(t *testing.T) {
	stray := errors.New("stray sentinel")
	got := userErrors.Unmapped(errNotFound, errConflict, stray, &forbiddenError{}, errWeak, errLocked)
	if len(got) != 1 || !errors.Is(got[0], stray) {
		t.Fatalf("Unmapped = %v, want [%v]", got, stray)
	}
	if got := userErrors.Unmapped(errNotFound); got != nil {
		t.Fatalf("a complete table must give nothing: %v", got)
	}
}

// Retry-After — из RetryAfter() в цепочке доменной ошибки; у 429 без неё — секунда.
func TestErrorMap_RetryAfter(t *testing.T) {
	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Get(api, "/locked/{secs}", func(_ context.Context, in *struct {
			Secs int `path:"secs"`
		},
		) (*struct{}, error) {
			if in.Secs == 0 {
				return nil, userErrors.Map(errLocked)
			}
			return nil, userErrors.Map(fmt.Errorf("login: %w", &lockedError{wait: time.Duration(in.Secs)*time.Second - 100*time.Millisecond}))
		})
	})
	for path, want := range map[string]string{"/locked/90": "90", "/locked/0": "1"} {
		resp := api.Get(path)
		handlertest.AssertAPIError(t, resp, http.StatusTooManyRequests, "AUTH_LOCKED")
		if got := resp.Header().Get("Retry-After"); got != want {
			t.Errorf("%s: Retry-After = %q, want %q", path, got, want)
		}
	}
}

// Несопоставленная ошибка — 500 без причины и строка handler error с operation в логе.
func TestErrorMap_UnmappedIs500WithOperation(t *testing.T) {
	logs := &syncBuffer{}
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(logs), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	srv := startApp(t,
		fx.Decorate(func(logkit.Logger) logkit.Logger { return log }),
		fx.Invoke(func(api huma.API) {
			huma.Register(api, userErrors.Declare(huma.Operation{OperationID: "getUser", Method: http.MethodGet, Path: "/users/{id}"}),
				func(context.Context, *struct {
					ID string `path:"id"`
				},
				) (*struct{}, error) {
					return nil, userErrors.Map(errors.New("pq: password=s3cret"))
				})
		}),
	)
	resp := get(t, srv, "/users/42")
	assertProblem(t, resp, contractCase{status: http.StatusInternalServerError, code: "INTERNAL", logged: true}, logs)
	var found bool
	for _, rec := range logs.lines(t) {
		if rec["msg"] == "handler error" && rec["operation"] == "getUser" && strings.Contains(fmt.Sprint(rec["error"]), "pq: password=") {
			found = true
		}
	}
	if !found {
		t.Fatalf("handler error with operation getUser and the cause must be logged")
	}
	for _, rec := range logs.lines(t) {
		if strings.Contains(fmt.Sprint(rec), "s3cret") {
			t.Fatalf("the password of the cause must be masked in the log: %v", rec)
		}
	}
}

// Declare кладёт статусы таблицы в операцию, а сервер — коды в описание ответов openapi.json.
func TestErrorMap_Declare(t *testing.T) {
	authErrors := httpkit.NewErrorMap(
		httpkit.ErrorRule{Err: errLocked, Status: http.StatusTooManyRequests, Code: "AUTH_LOCKED"},
		httpkit.ErrorRule{Err: errConflict, Status: http.StatusConflict, Code: "AUTH_SESSION_CONFLICT"},
	)
	// Запас ёмкости у Errors: append без клона писал бы статусы в массив вызывающего.
	base := huma.Operation{
		OperationID: "updateUser", Method: http.MethodPatch, Path: "/users/{id}",
		Errors: append(make([]int, 0, 8), http.StatusUnauthorized),
	}
	op := authErrors.Declare(userErrors.Declare(base))
	if len(base.Errors) != 1 || base.Metadata != nil || slices.ContainsFunc(base.Errors[1:cap(base.Errors)], func(s int) bool { return s != 0 }) {
		t.Fatalf("Declare must not change the caller's operation: %+v %v", base, base.Errors[:cap(base.Errors)])
	}

	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Register(api, op, func(context.Context, *struct {
			ID   string `path:"id"`
			Body struct {
				Name string `json:"name"`
			}
		},
		) (*struct{}, error) {
			return nil, nil // тело ответа не нужно
		})
	})
	raw, err := json.Marshal(api.OpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Description string `json:"description"`
				Content     map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	responses := spec.Paths["/users/{id}"]["patch"].Responses
	want := map[string]string{
		"401": "Unauthorized",
		"403": "Forbidden: FORBIDDEN",
		"404": "Not Found: USER_NOT_FOUND",
		"409": "Conflict: USER_CONCURRENT_UPDATE, AUTH_SESSION_CONFLICT",
		"422": "Unprocessable Entity: USER_WEAK_PASSWORD",
		"429": "Too Many Requests: AUTH_LOCKED, RATE_LIMITED", // лимитер сервера по умолчанию включён
		"500": "Internal Server Error",
	}
	for status, desc := range want {
		resp, ok := responses[status]
		if !ok || resp.Description != desc {
			t.Errorf("response %s = %q, want %q", status, resp.Description, desc)
		}
		if ref := resp.Content[httpkit.ProblemContentType].Schema.Ref; !strings.HasSuffix(ref, "/APIError") {
			t.Errorf("response %s schema = %q, want APIError", status, ref)
		}
	}
}

// N7 валидатора: операция с Security объявляет 401 HumaAuth в OpenAPI сама, коды
// Declare для 401 — рядом; у публичной операции 401 нет.
func TestErrorMap_DeclareSecured401(t *testing.T) {
	authErrors := httpkit.NewErrorMap(
		httpkit.ErrorRule{Err: errLocked, Status: http.StatusUnauthorized, Code: "AUTH_TOKEN_EXPIRED"},
	)
	secured := []map[string][]string{{"Bearer": {}}}
	noBody := func(context.Context, *struct{}) (*struct{}, error) { return nil, nil } // тело ответа не нужно
	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Register(api, huma.Operation{OperationID: "getMe", Method: http.MethodGet, Path: "/me", Security: secured}, noBody)
		huma.Register(api, authErrors.Declare(huma.Operation{OperationID: "refresh", Method: http.MethodPost, Path: "/refresh", Security: secured}), noBody)
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "status", Method: http.MethodGet, Path: "/status", Security: []map[string][]string{}}), noBody)
	}, handlertest.WithSecuritySchemes(httpkit.SecuritySchemeEntry{
		Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"},
		Extract: func(r *http.Request) (context.Context, error) { return r.Context(), nil },
	}))

	for path, want := range map[string]string{
		"/me":      "Unauthorized: UNAUTHORIZED",
		"/refresh": "Unauthorized: AUTH_TOKEN_EXPIRED, UNAUTHORIZED",
	} {
		item := api.OpenAPI().Paths[path]
		op := item.Get
		if op == nil {
			op = item.Post
		}
		resp := op.Responses["401"]
		if resp == nil || resp.Description != want {
			t.Fatalf("%s: 401 = %+v, want %q", path, resp, want)
		}
		if ref := resp.Content[httpkit.ProblemContentType].Schema.Ref; !strings.HasSuffix(ref, "/APIError") {
			t.Errorf("%s: 401 schema = %q, want APIError", path, ref)
		}
	}
	if resp := api.OpenAPI().Paths["/status"].Get.Responses["401"]; resp != nil {
		t.Fatalf("a public operation must not declare 401: %+v", resp)
	}
}

// Security, выставленный хуком OnAddOperation потребителя, тоже объявляет 401: описание
// ошибок — последний хук сервера.
func TestErrorMap_DeclareSecured401FromConsumerHook(t *testing.T) {
	secureAdmin := httpkit.WithHumaConfig(func(c *huma.Config) {
		c.OnAddOperation = append(c.OnAddOperation, func(_ *huma.OpenAPI, op *huma.Operation) {
			if strings.HasPrefix(op.Path, "/admin/") {
				op.Security = []map[string][]string{{"Bearer": {}}}
			}
		})
	})
	api := mustServer(t, testConfig(t, "127.0.0.1:0"), secureAdmin).BindAPI()
	huma.Get(api, "/admin/users", func(context.Context, *struct{}) (*struct{}, error) { return nil, nil }) // тело ответа не нужно

	resp := api.OpenAPI().Paths["/admin/users"].Get.Responses["401"]
	if resp == nil || resp.Description != "Unauthorized: UNAUTHORIZED" {
		t.Fatalf("401 = %+v, want %q", resp, "Unauthorized: UNAUTHORIZED")
	}
}

// Регрессия golibinfra-httpkit-429: лимитер сервера отвечает 429 на любой операции, а контракт знал 429 только из
// таблиц. При HTTP_RATE_LIMIT_RPS > 0 каждая операция объявляет 429 RATE_LIMITED — код и Retry-After те, что
// отдаёт лимитер; коды 429 таблицы — первыми и без повтора; при 0 — только 429 таблицы; Retry-After — у любого 429.
func TestErrorMap_DescribesServerRateLimit(t *testing.T) {
	limitErrors := httpkit.NewErrorMap( // свой лимитер потребителя на части путей
		httpkit.ErrorRule{Err: errRateLimited, Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", Detail: "rate limit exceeded"},
	)
	lockErrors := httpkit.NewErrorMap(
		httpkit.ErrorRule{Err: errLocked, Status: http.StatusTooManyRequests, Code: "AUTH_LOCKED"},
	)
	bearer := httpkit.SecuritySchemeEntry{
		Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"},
		Extract: func(r *http.Request) (context.Context, error) { return r.Context(), nil },
	}
	noBody := func(context.Context, *struct{}) (*struct{}, error) { return nil, nil } // тело ответа не нужно
	register := func(api huma.API) {
		httpkit.RegisterHealth(api, time.Second, nil)
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "ping", Method: http.MethodGet, Path: "/ping"}), noBody)
		huma.Register(api, huma.Operation{
			OperationID: "getMe", Method: http.MethodGet, Path: "/me", Security: []map[string][]string{{"Bearer": {}}},
		}, noBody)
		huma.Register(api, lockErrors.Declare(httpkit.Public(huma.Operation{OperationID: "login", Method: http.MethodPost, Path: "/login"})), noBody)
		huma.Register(api, limitErrors.Declare(httpkit.Public(huma.Operation{OperationID: "logout", Method: http.MethodPost, Path: "/logout"})), noBody)
		// Маршрут мимо huma, описанный в OpenAPI руками: ответов у операции нет.
		api.OpenAPI().AddOperation(&huma.Operation{OperationID: "stream", Method: http.MethodGet, Path: "/stream"})
	}
	const limited = "Too Many Requests: RATE_LIMITED"
	for _, tc := range []struct {
		name string
		rps  int
		want map[string]string // операция → описание 429; "" — ответа 429 нет
	}{
		{"limiter on", 1, map[string]string{
			"get-health": limited, "get-ready": limited, "ping": limited, "getMe": limited, "stream": limited,
			"login": "Too Many Requests: AUTH_LOCKED, RATE_LIMITED", "logout": limited,
		}},
		{"limiter off", 0, map[string]string{
			"get-health": "", "get-ready": "", "ping": "", "getMe": "", "stream": "",
			"login": "Too Many Requests: AUTH_LOCKED", "logout": limited,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "127.0.0.1:0")
			cfg.RateLimit = httpkit.RateLimitConfig{RPS: tc.rps, Burst: tc.rps}
			api := handlertest.NewAPI(t, register, handlertest.WithConfig(cfg), handlertest.WithSecuritySchemes(bearer))
			ops := operations(api.OpenAPI())
			for id, want := range tc.want {
				op := ops[id]
				if op == nil {
					t.Fatalf("operation %s is not in OpenAPI", id)
				}
				resp := op.Responses[strconv.Itoa(http.StatusTooManyRequests)]
				if want == "" {
					if resp != nil {
						t.Errorf("%s: 429 = %q without a limiter or a table rule", id, resp.Description)
					}
					continue
				}
				if resp == nil || resp.Description != want {
					t.Errorf("%s: 429 = %+v, want %q", id, resp, want)
					continue
				}
				if ref := resp.Content[httpkit.ProblemContentType].Schema.Ref; !strings.HasSuffix(ref, "/APIError") {
					t.Errorf("%s: 429 schema = %q, want APIError", id, ref)
				}
				h := resp.Headers["Retry-After"]
				if h == nil || !h.Required || h.Schema == nil || h.Schema.Type != huma.TypeInteger || h.Schema.Minimum == nil || *h.Schema.Minimum != 1 {
					t.Errorf("%s: 429 must declare a required integer Retry-After >= 1, got %+v", id, h)
				}
			}
			if tc.rps == 0 {
				return
			}
			// Описание — то, что лимитер отвечает на этой же операции.
			api.Get("/ping")
			resp := api.Get("/ping")
			got := handlertest.DecodeAPIError(t, resp)
			handlertest.AssertAPIError(t, resp, http.StatusTooManyRequests, got.Code)
			if codes := codesOf(ops["ping"], http.StatusTooManyRequests); !slices.Contains(codes, got.Code) {
				t.Errorf("the limiter answers %s, the contract of ping lists %v", got.Code, codes)
			}
			if secs, err := strconv.Atoi(resp.Header().Get("Retry-After")); err != nil || secs < 1 {
				t.Errorf("the limiter answers Retry-After %q, the contract promises an integer >= 1", resp.Header().Get("Retry-After"))
			}
		})
	}
}

// Регрессия httpkit-kit-errors-contract: ответы, которые кит отдаёт на операции сам, — в контракте там и тогда, где он
// их отдаёт: 413 — у операции huma с телом под лимитом (у multipart превышение — 422 валидации), 503 REQUEST_TIMEOUT —
// при дедлайне запроса, кроме потока SSE, 401 HumaAuth — с WWW-Authenticate по схемам операции и только при
// обязательной аутентификации; у маршрута мимо huma 401 и 413 — ответы его хендлера. 503 AUTH_UNAVAILABLE отдаёт
// extractor схемы — его объявляет таблица владельца схемы, кит код не повторяет. Описание сверено с ответом кита.
func TestErrorMap_DescribesKitResponses(t *testing.T) {
	bearer := httpkit.SecuritySchemeEntry{
		Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"},
		Extract: func(r *http.Request) (context.Context, error) {
			switch r.Header.Get("Authorization") {
			case "":
				return nil, errors.New("no credentials")
			case "Bearer down":
				return nil, fmt.Errorf("jwks: %w", httpkit.ErrAuthUnavailable)
			}
			return r.Context(), nil
		},
	}
	backendErrors := httpkit.NewErrorMap( // таблица владельца схемы, чей extractor знает недоступный бэкенд
		httpkit.ErrorRule{Err: httpkit.ErrAuthUnavailable, Status: http.StatusServiceUnavailable, Code: "AUTH_UNAVAILABLE"},
	)
	bodyErrors := httpkit.NewErrorMap(
		httpkit.ErrorRule{Err: errTooLarge, Status: http.StatusRequestEntityTooLarge, Code: "BODY_TOO_LARGE"},
	)
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
	noBody := func(context.Context, *struct{}) (*struct{}, error) { return nil, nil } // тело ответа не нужно
	noOutput := func(context.Context, *named) (*struct{}, error) { return nil, nil }
	secured := []map[string][]string{{"Bearer": {}}}
	register := func(api huma.API) {
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "upload", Method: http.MethodPost, Path: "/upload"}), noOutput)
		huma.Register(api, bodyErrors.Declare(httpkit.Public(huma.Operation{OperationID: "import", Method: http.MethodPost, Path: "/import"})), noOutput)
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "raw", Method: http.MethodPost, Path: "/raw"}),
			func(context.Context, *struct{ RawBody []byte }) (*struct{}, error) { return nil, nil })
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "form", Method: http.MethodPost, Path: "/form"}),
			func(context.Context, *form) (*struct{}, error) { return nil, nil })
		huma.Register(api, httpkit.Public(huma.Operation{OperationID: "slow", Method: http.MethodGet, Path: "/slow"}),
			func(ctx context.Context, _ *struct{}) (*struct{}, error) {
				<-ctx.Done()
				return nil, nil
			})
		sse.Register(api, httpkit.Public(huma.Operation{OperationID: "events", Method: http.MethodGet, Path: "/events"}),
			map[string]any{"tick": struct{}{}}, func(context.Context, *struct{}, sse.Sender) {})
		huma.Register(api, huma.Operation{OperationID: "me", Method: http.MethodGet, Path: "/me", Security: secured}, noBody)
		huma.Register(api, backendErrors.Declare(huma.Operation{OperationID: "hub", Method: http.MethodGet, Path: "/hub", Security: secured}), noBody)
		huma.Register(api, huma.Operation{
			OperationID: "maybe", Method: http.MethodGet, Path: "/maybe", Security: []map[string][]string{{"Bearer": {}}, {}},
		}, noBody)
		// Маршрут мимо huma с телом и Security, описанный в OpenAPI руками: HumaAuth его не проверяет, на
		// тело сверх лимита отвечает его хендлер.
		api.OpenAPI().AddOperation(&huma.Operation{
			OperationID: "rawRoute", Method: http.MethodPost, Path: "/raw-route", Security: secured,
			RequestBody: &huma.RequestBody{Content: map[string]*huma.MediaType{"application/json": {}}},
		})
	}
	const (
		tooLarge     = "Request Entity Too Large: BODY_TOO_LARGE"
		timeout      = "Service Unavailable: REQUEST_TIMEOUT"
		unauthorized = "Unauthorized: UNAUTHORIZED"
	)
	type described struct{ s413, s503, s401 string } // описания ответов; "" — ответа нет
	for _, tc := range []struct {
		name   string
		limits bool
		want   map[string]described
	}{
		{"limits on", true, map[string]described{
			"upload": {tooLarge, timeout, ""}, "import": {tooLarge, timeout, ""}, "raw": {tooLarge, timeout, ""},
			"form": {"", timeout, ""}, "slow": {"", timeout, ""}, "events": {}, "rawRoute": {"", timeout, ""},
			"me": {"", timeout, unauthorized}, "maybe": {"", timeout, ""},
			"hub": {"", "Service Unavailable: AUTH_UNAVAILABLE, REQUEST_TIMEOUT", unauthorized},
		}},
		{"limits off", false, map[string]described{
			"upload": {}, "import": {tooLarge, "", ""}, "raw": {}, "form": {}, "slow": {}, "events": {}, "rawRoute": {},
			"me": {"", "", unauthorized}, "maybe": {}, "hub": {"", "Service Unavailable: AUTH_UNAVAILABLE", unauthorized},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "127.0.0.1:0")
			cfg.RateLimit.RPS, cfg.MaxBody.MaxBytes, cfg.RequestTimeout = 0, 0, -time.Second
			if tc.limits {
				cfg.MaxBody.MaxBytes, cfg.RequestTimeout = 64, 100*time.Millisecond
			}
			api := handlertest.NewAPI(t, register, handlertest.WithConfig(cfg), handlertest.WithSecuritySchemes(bearer))
			ops := operations(api.OpenAPI())
			for id, want := range tc.want {
				op := ops[id]
				if op == nil {
					t.Fatalf("operation %s is not in OpenAPI", id)
				}
				for status, desc := range map[int]string{
					http.StatusRequestEntityTooLarge: want.s413, http.StatusServiceUnavailable: want.s503, http.StatusUnauthorized: want.s401,
				} {
					resp, got := op.Responses[strconv.Itoa(status)], ""
					if resp != nil {
						got = resp.Description
					}
					if got != desc {
						t.Errorf("%s: %d = %q, want %q", id, status, got, desc)
						continue
					}
					if resp != nil && !strings.HasSuffix(resp.Content[httpkit.ProblemContentType].Schema.Ref, "/APIError") {
						t.Errorf("%s: %d is not an APIError", id, status)
					}
				}
				if want.s401 == "" {
					continue
				}
				h := op.Responses["401"].Headers["WWW-Authenticate"]
				if h == nil || h.Required || h.Schema == nil || h.Schema.Type != huma.TypeString || h.Example != "Bearer" {
					t.Errorf("%s: 401 must declare the optional WWW-Authenticate challenge Bearer, got %+v", id, h)
				}
			}
			if !tc.limits {
				return
			}
			// Описание — то, что кит отвечает на этой же операции.
			answers := func(id string, status int, resp *httptest.ResponseRecorder) {
				t.Helper()
				got := handlertest.DecodeAPIError(t, resp)
				handlertest.AssertAPIError(t, resp, status, got.Code)
				if codes := codesOf(ops[id], status); !slices.Contains(codes, got.Code) {
					t.Errorf("the kit answers %s %d %s, the contract lists %v", id, status, got.Code, codes)
				}
			}
			answers("upload", http.StatusRequestEntityTooLarge, api.Post("/upload", strings.NewReader(`{"name":"`+strings.Repeat("x", 100)+`"}`)))
			answers("slow", http.StatusServiceUnavailable, api.Get("/slow"))
			answers("hub", http.StatusServiceUnavailable, api.Get("/hub", "Authorization: Bearer down"))
			resp := api.Get("/me")
			answers("me", http.StatusUnauthorized, resp)
			var promised any
			if h := ops["me"].Responses["401"].Headers["WWW-Authenticate"]; h != nil {
				promised = h.Example
			}
			if got := resp.Header().Get("WWW-Authenticate"); got != promised {
				t.Errorf("HumaAuth answers WWW-Authenticate %q, the contract of me promises %v", got, promised)
			}
			// Превышение у multipart — 422 валидации huma, поэтому 413 у формы в контракте нет.
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			fw, err := mw.CreateFormFile("file", "a.txt")
			if err == nil {
				_, err = fw.Write([]byte(strings.Repeat("y", 200)))
			}
			if err != nil || mw.Close() != nil {
				t.Fatalf("multipart body: %v", err)
			}
			handlertest.AssertAPIError(t, api.Post("/form", "Content-Type: "+mw.FormDataContentType(), &body), http.StatusUnprocessableEntity, "VALIDATION_FAILED")
			// Необязательная аутентификация: без credentials запрос идёт в хендлер, а не в 401.
			if resp := api.Get("/maybe"); resp.Code >= http.StatusBadRequest {
				t.Errorf("an optional-auth operation without credentials: %d, want the handler response", resp.Code)
			}
		})
	}
}

// Регрессия httpkit-ready-503-contract: /ready отвечает 503 своим телом готовности — draining на паузе остановки,
// unhealthy при пробе down, — а при дедлайне запроса — 503 REQUEST_TIMEOUT: у ответа 503 в контракте оба тела
// (ReadyBody и APIError), коды не повторяются; без дедлайна — только тело готовности. Описание сверено с ответом /ready.
func TestErrorMap_DescribesReady503(t *testing.T) {
	t.Setenv("HTTP_REQUEST_TIMEOUT", "300ms")
	t.Setenv("HTTP_SHUTDOWN_DRAIN_DELAY", "400ms")
	var mode atomic.Int32 // проба: 0 — ok, 1 — down, 2 — висит до дедлайна запроса
	probe := healthkit.New("dep", func(ctx context.Context) error {
		switch mode.Load() {
		case 1:
			return errors.New("dep is down")
		case 2:
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	var srv *httpkit.Server
	app := fx.New(fx.NopLogger, fx.Supply(testConfig(t, "127.0.0.1:0")), fx.Provide(logkit.Nop), httpkit.Module,
		healthkit.ProvideProbe(func() healthkit.Probe { return probe }), fx.Populate(&srv))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	ready := operations(srv.API().OpenAPI())["get-ready"]
	described := ready.Responses["503"]
	if described == nil {
		t.Fatal("/ready has no 503 in OpenAPI")
	}
	if want := "Service Unavailable: draining, unhealthy, REQUEST_TIMEOUT"; described.Description != want {
		t.Errorf("503 of /ready = %q, want %q", described.Description, want)
	}
	for ct, schema := range map[string]string{"application/json": "/ReadyBody", httpkit.ProblemContentType: "/APIError"} {
		if mt := described.Content[ct]; mt == nil || mt.Schema == nil || !strings.HasSuffix(mt.Schema.Ref, schema) {
			t.Errorf("503 of /ready: %s must be %s, got %+v", ct, schema, mt)
		}
	}
	addr := srv.Addr().String()
	check := func(name string, status int, contentType, code string) {
		t.Helper()
		if status != http.StatusServiceUnavailable || described.Content[contentType] == nil ||
			!slices.Contains(codesOf(ready, http.StatusServiceUnavailable), code) {
			t.Errorf("%s: /ready answers %d %s %q, the contract of its 503 is %q with %v",
				name, status, contentType, code, described.Description, slices.Collect(maps.Keys(described.Content)))
		}
	}
	for _, c := range []struct {
		name string
		mode int32
		want string
	}{{"probe down", 1, "unhealthy"}, {"request deadline", 2, "REQUEST_TIMEOUT"}} {
		mode.Store(c.mode)
		status, ct, code := readyAnswer(t, addr)
		if code != c.want {
			t.Errorf("%s: /ready answers %q, want %q", c.name, code, c.want)
		}
		check(c.name, status, ct, code)
	}

	mode.Store(0)
	stopped := make(chan error, 1)
	go func() { stopped <- app.Stop(ctx) }()
	status, ct, code := readyAnswer(t, addr)
	for wait := time.Now().Add(time.Second); status != http.StatusServiceUnavailable && time.Now().Before(wait); {
		status, ct, code = readyAnswer(t, addr)
	}
	if code != "draining" {
		t.Errorf("drain pause: /ready answers %d %q, want 503 draining", status, code)
	}
	check("drain pause", status, ct, code)
	if err := <-stopped; err != nil {
		t.Fatalf("stop: %v", err)
	}

	cfg := testConfig(t, "127.0.0.1:0")
	cfg.RequestTimeout = -time.Second
	api := handlertest.NewAPI(t, func(api huma.API) { httpkit.RegisterHealth(api, time.Second, nil) }, handlertest.WithConfig(cfg))
	off := operations(api.OpenAPI())["get-ready"].Responses["503"]
	if off == nil || off.Description != "Service Unavailable: draining, unhealthy" || off.Content[httpkit.ProblemContentType] != nil {
		t.Errorf("503 of /ready without a request deadline = %+v, want only the readiness body", off)
	}
}

// readyAnswer — ответ /ready: статус, тип тела и его код — status тела готовности или code APIError.
func readyAnswer(t *testing.T, addr string) (status int, contentType, code string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/ready", http.NoBody)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /ready: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	contentType, _, _ = mime.ParseMediaType(resp.Header.Get("Content-Type"))
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /ready: %v", err)
	}
	key := "status"
	if contentType == httpkit.ProblemContentType {
		key = "code"
	}
	code, _ = body[key].(string)
	return resp.StatusCode, contentType, code
}

// operations — операции OpenAPI по OperationID.
func operations(oapi *huma.OpenAPI) map[string]*huma.Operation {
	ops := map[string]*huma.Operation{}
	for _, item := range oapi.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op != nil {
				ops[op.OperationID] = op
			}
		}
	}
	return ops
}

// codesOf — коды из описания ответа status операции (`<текст статуса>: A, B`); ответа нет — nil.
func codesOf(op *huma.Operation, status int) []string {
	resp := op.Responses[strconv.Itoa(status)]
	if resp == nil {
		return nil
	}
	_, list, _ := strings.Cut(resp.Description, ": ")
	return strings.Split(list, ", ")
}

func TestErrorMap_InvalidRulePanics(t *testing.T) {
	sentinel := errors.New("x")
	specific := fmt.Errorf("%w: reused", sentinel)
	cases := map[string][]httpkit.ErrorRule{
		"без Err и Match":   {{Status: http.StatusNotFound, Code: "X"}},
		"и Err, и Match":    {{Err: sentinel, Match: isForbidden, Status: http.StatusNotFound, Code: "X"}},
		"без Code":          {{Err: sentinel, Status: http.StatusNotFound}},
		"статус не ошибка":  {{Err: sentinel, Status: http.StatusOK, Code: "X"}},
		"статус вне границ": {{Err: sentinel, Status: 600, Code: "X"}},
		"затенённое правило": {
			{Err: sentinel, Status: http.StatusUnauthorized, Code: "GENERAL"},
			{Err: specific, Status: http.StatusUnauthorized, Code: "SPECIFIC"},
		},
		"затенённое предикатом": {
			{Match: func(error) bool { return true }, Status: http.StatusBadRequest, Code: "ANY"},
			{Err: sentinel, Status: http.StatusNotFound, Code: "X"},
		},
	}
	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewErrorMap must panic on an invalid table")
				}
			}()
			httpkit.NewErrorMap(rules...)
		})
	}
	// Частное правило выше общего — законная таблица.
	httpkit.NewErrorMap(
		httpkit.ErrorRule{Err: specific, Status: http.StatusUnauthorized, Code: "SPECIFIC"},
		httpkit.ErrorRule{Err: sentinel, Status: http.StatusUnauthorized, Code: "GENERAL"},
	)
}
