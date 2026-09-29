package httpkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/testkit/handlertest"
)

func TestAPIError_JSON(t *testing.T) {
	raw, err := json.Marshal(httpkit.Error("USER_NOT_FOUND", http.StatusNotFound, "user not found"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"code":"USER_NOT_FOUND"`, `"status":404`, `"title":"Not Found"`, `"detail":"user not found"`} {
		if !strings.Contains(s, want) {
			t.Errorf("json %s lacks %s", s, want)
		}
	}
	for _, absent := range []string{`"request_id"`, `"errors"`} {
		if strings.Contains(s, absent) {
			t.Errorf("json %s must omit empty %s", s, absent)
		}
	}
}

// Ошибки самой huma — APIError кита: процесс, импортировавший httpkit, отдаёт одну форму.
func TestHumaErrors_AreAPIErrors(t *testing.T) {
	var apiErr *httpkit.APIError
	if !errors.As(huma.Error404NotFound("no such thing"), &apiErr) || apiErr.Code != "NOT_FOUND" || apiErr.Detail != "no such thing" {
		t.Fatalf("huma.Error404NotFound: %#v", apiErr)
	}

	validation := huma.NewError(http.StatusUnprocessableEntity, "validation failed",
		&huma.ErrorDetail{Location: "body.password", Message: "expected length >= 12", Value: "hunter2"},
		errors.New("end must follow start"),
	)
	if !errors.As(validation, &apiErr) || apiErr.Code != "VALIDATION_FAILED" {
		t.Fatalf("validation: %#v", validation)
	}
	want := []httpkit.FieldError{
		{Location: "body.password", Code: "INVALID", Message: "expected length >= 12"},
		{Code: "INVALID", Message: "end must follow start"},
	}
	if len(apiErr.Errors) != len(want) || apiErr.Errors[0] != want[0] || apiErr.Errors[1] != want[1] {
		t.Fatalf("Errors = %+v, want %+v", apiErr.Errors, want)
	}
	if raw, _ := json.Marshal(apiErr); strings.Contains(string(raw), "hunter2") {
		t.Fatalf("the field value must not be echoed: %s", raw)
	}

	internal := huma.NewErrorWithContext(nil, http.StatusInternalServerError, "unexpected error occurred", errors.New("pq: password=s3cret"))
	if !errors.As(internal, &apiErr) || apiErr.Code != "INTERNAL" || apiErr.Detail != "" || len(apiErr.Errors) != 0 {
		t.Fatalf("5xx must be neutral: %#v", internal)
	}
	if raw, _ := json.Marshal(apiErr); strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "unexpected") {
		t.Fatalf("5xx body must not carry the cause: %s", raw)
	}

	// defineErrors берёт схему из NewError(0, ""), а Status304NotModified — не ошибка.
	if e := huma.NewError(0, ""); e == nil {
		t.Fatal("NewError(0) must build the schema example")
	}
	if got := huma.Status304NotModified().GetStatus(); got != http.StatusNotModified {
		t.Fatalf("304 status = %d", got)
	}
}

// huma.ErrorWithHeaders дописывает заголовки к APIError, а не теряет их; свой
// Retry-After у 429 кит не перетирает вычисленным.
func TestAPIError_WithHumaHeaders(t *testing.T) {
	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Get(api, "/doc", func(context.Context, *struct{}) (*struct{}, error) {
			return nil, huma.ErrorWithHeaders(httpkit.Conflict("DOC_CHANGED", "document changed"), http.Header{"ETag": {`"v2"`}})
		})
		huma.Get(api, "/slow", func(context.Context, *struct{}) (*struct{}, error) {
			return nil, huma.ErrorWithHeaders(huma.Error429TooManyRequests("slow down"), http.Header{"Retry-After": {"120"}})
		})
	})
	resp := api.Get("/doc")
	handlertest.AssertAPIError(t, resp, http.StatusConflict, "DOC_CHANGED")
	if got := resp.Header().Get("ETag"); got != `"v2"` {
		t.Fatalf("ETag = %q", got)
	}

	resp = api.Get("/slow")
	handlertest.AssertAPIError(t, resp, http.StatusTooManyRequests, "RATE_LIMITED")
	if got := resp.Header().Values("Retry-After"); len(got) != 1 || got[0] != "120" {
		t.Fatalf("Retry-After = %v, want the handler's [120]", got)
	}
}

// Сообщения форматов huma, цитирующие ввод, обрезаются до базового текста: значение
// поля (в нём бывает пароль) клиенту не возвращается.
func TestHumaErrors_FormatMessagesDoNotEchoInput(t *testing.T) {
	type formats struct {
		Body struct {
			Email    string `json:"email" format:"email"`
			URI      string `json:"uri" format:"uri"`
			UUID     string `json:"uuid" format:"uuid"`
			Regex    string `json:"regex" format:"regex"`
			Duration string `json:"duration" format:"duration"`
		}
	}
	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Post(api, "/formats", func(context.Context, *formats) (*struct{}, error) { return nil, nil }) //nolint:nilnil // тело ответа не нужно
	})
	resp := api.Post("/formats", map[string]any{
		"email":    "a@b.c, S3CRETmail@x.y",
		"uri":      "http://S3CRETuri%zz",
		"uuid":     "S3CRETxyz" + strings.Repeat("0", 36),
		"regex":    "(S3CRETre",
		"duration": "S3CRETdur",
	})
	handlertest.AssertAPIError(t, resp, http.StatusUnprocessableEntity, "VALIDATION_FAILED")
	if strings.Contains(resp.Body.String(), "S3CRET") {
		t.Fatalf("the input is echoed: %s", resp.Body.String())
	}
	want := map[string]string{
		"body.email":    "expected string to be RFC 5322 email",
		"body.uri":      "expected string to be RFC 3986 uri",
		"body.uuid":     "expected string to be RFC 4122 uuid",
		"body.regex":    "expected string to be regex",
		"body.duration": "expected duration",
	}
	got := map[string]string{}
	for _, fe := range handlertest.DecodeAPIError(t, resp).Errors {
		got[fe.Location] = fe.Message
	}
	for loc, msg := range want {
		if got[loc] != msg {
			t.Errorf("%s: message %q, want %q", loc, got[loc], msg)
		}
	}
}

var errShared = httpkit.Conflict("SHARED", "shared package-level error")

// Общий package-level APIError под конкурентными запросами: каждый ответ несёт свой
// request_id, а сам общий объект не меняется.
func TestAPIError_SharedValueKeepsOwnRequestID(t *testing.T) {
	api := handlertest.NewAPI(t, func(api huma.API) {
		huma.Get(api, "/shared", func(context.Context, *struct{}) (*struct{}, error) { return nil, errShared })
	})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			id := fmt.Sprintf("req-%d", i)
			var got httpkit.APIError
			if err := json.Unmarshal(api.Get("/shared", "X-Request-ID: "+id).Body.Bytes(), &got); err != nil || got.RequestID != id {
				t.Errorf("request_id = %q (%v), want %q", got.RequestID, err, id)
			}
		})
	}
	wg.Wait()
	if errShared.RequestID != "" {
		t.Fatalf("the shared APIError was mutated: request_id %q", errShared.RequestID)
	}
}

func TestAPIError_GetStatusAndError(t *testing.T) {
	e := httpkit.Conflict("DUP", "already exists")
	if e.GetStatus() != http.StatusConflict {
		t.Fatalf("status %d", e.GetStatus())
	}
	if e.Error() != "DUP: already exists" {
		t.Fatalf("error %q", e.Error())
	}
}

func TestErrorHelpers(t *testing.T) {
	cases := []struct {
		got    *httpkit.APIError
		status int
	}{
		{httpkit.BadRequest("C", "d"), http.StatusBadRequest},
		{httpkit.Unauthorized("C", "d"), http.StatusUnauthorized},
		{httpkit.Forbidden("C", "d"), http.StatusForbidden},
		{httpkit.NotFound("C", "d"), http.StatusNotFound},
		{httpkit.Conflict("C", "d"), http.StatusConflict},
		{httpkit.Internal("C", "d"), http.StatusInternalServerError},
		{httpkit.Unavailable("C", "d"), http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		if c.got.Status != c.status || c.got.Title != http.StatusText(c.status) {
			t.Errorf("%+v: want status %d", c.got, c.status)
		}
	}
}

func TestAPIError_ContentType(t *testing.T) {
	e := httpkit.NotFound("C", "d")
	if got := e.ContentType("application/json"); got != httpkit.ProblemContentType {
		t.Errorf("json must become problem+json, got %q", got)
	}
	if got := e.ContentType("application/cbor"); got != "application/cbor" {
		t.Errorf("other content types stay as is, got %q", got)
	}
}
