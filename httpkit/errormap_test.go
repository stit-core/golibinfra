package httpkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
)

var (
	errNotFound = errors.New("user not found")
	errConflict = errors.New("concurrent update")
	errWeak     = errors.New("weak password")
	errLocked   = errors.New("account locked")
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
		if rec["msg"] == "handler error" && rec["operation"] == "getUser" && strings.Contains(fmt.Sprint(rec["err"]), "s3cret") {
			found = true
		}
	}
	if !found {
		t.Fatalf("handler error with operation getUser and the cause must be logged")
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
			return nil, nil //nolint:nilnil // тело ответа не нужно
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
		"429": "Too Many Requests: AUTH_LOCKED",
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
	noBody := func(context.Context, *struct{}) (*struct{}, error) { return nil, nil } //nolint:nilnil // тело ответа не нужно
	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Register(api, huma.Operation{OperationID: "getMe", Method: http.MethodGet, Path: "/me", Security: secured}, noBody)
		huma.Register(api, authErrors.Declare(huma.Operation{OperationID: "refresh", Method: http.MethodPost, Path: "/refresh", Security: secured}), noBody)
		huma.Register(api, huma.Operation{OperationID: "status", Method: http.MethodGet, Path: "/status", Security: []map[string][]string{}}, noBody)
	})

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
	huma.Get(api, "/admin/users", func(context.Context, *struct{}) (*struct{}, error) { return nil, nil }) //nolint:nilnil // тело ответа не нужно

	resp := api.OpenAPI().Paths["/admin/users"].Get.Responses["401"]
	if resp == nil || resp.Description != "Unauthorized: UNAUTHORIZED" {
		t.Fatalf("401 = %+v, want %q", resp, "Unauthorized: UNAUTHORIZED")
	}
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
