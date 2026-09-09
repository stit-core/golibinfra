package handlertest_test

import (
	"context"
	"errors"
	"net/http"
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

// Плоское тело httpkit.APIError: code/status/title/detail доходят до теста.
func TestDecodeAPIError_FlatProblem(t *testing.T) {
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
	if len(got.Errors) != 0 {
		t.Errorf("Errors = %+v, want empty", got.Errors)
	}
}

// huma.ErrorModel на 422: top-level поля не теряются, errors[] доступен.
func TestDecodeAPIError_HumaValidation(t *testing.T) {
	resp := newAPI(t).Post("/users", map[string]any{"name": "ab"})
	handlertest.AssertStatus(t, resp, http.StatusUnprocessableEntity)

	got := handlertest.DecodeAPIError(t, resp)
	if got.Status != http.StatusUnprocessableEntity {
		t.Errorf("Status = %d, want %d", got.Status, http.StatusUnprocessableEntity)
	}
	if got.Title == "" || got.Detail == "" {
		t.Errorf("top-level fields lost: %+v", got)
	}
	if got.Code != "" {
		t.Errorf("Code = %q, want empty for huma validation errors", got.Code)
	}
	if len(got.Errors) == 0 {
		t.Fatalf("Errors is empty: %+v", got)
	}
	if got.Errors[0].Message == "" || got.Errors[0].Location == "" {
		t.Errorf("errors[0] = %+v, want message and location", got.Errors[0])
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
