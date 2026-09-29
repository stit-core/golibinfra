package handlertest_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if allow := resp.Header().Values("Allow"); len(allow) != 1 || allow[0] != http.MethodGet {
		t.Errorf("Allow = %v, want [GET]", allow)
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

// Регрессия: auth-гейт потребителя должен тестироваться на handlertest.NewAPI —
// httpkit.HumaAuth не завязан на конкретный huma-адаптер.
func TestNewAPI_WorksWithHumaAuth(t *testing.T) {
	api := handlertest.NewAPI(t, func(api huma.API) {
		api.UseMiddleware(httpkit.HumaAuth(logkit.Nop(), func(r *http.Request) (context.Context, error) {
			if r.Header.Get("Authorization") == "" {
				return nil, errors.New("no credentials")
			}
			return r.Context(), nil
		}))
		huma.Register(api, huma.Operation{
			OperationID: "get-secret",
			Method:      http.MethodGet,
			Path:        "/secret",
			Security:    []map[string][]string{{"Bearer": {}}},
		}, func(context.Context, *struct{}) (*emptyOutput, error) {
			return &emptyOutput{}, nil
		})
	})

	handlertest.AssertAPIError(t, api.Get("/secret"), http.StatusUnauthorized, "UNAUTHORIZED")
	handlertest.AssertStatus(t, api.Get("/secret", "Authorization: Bearer alice"), http.StatusNoContent)
}
