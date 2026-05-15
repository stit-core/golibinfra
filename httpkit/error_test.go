package httpkit_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.stit.tech/stit-infra/golibinfra/httpkit"
)

func TestAPIError_JSON(t *testing.T) {
	e := httpkit.Error("TEST_CODE", http.StatusBadRequest, "bad input")

	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec.Write(body)

	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	tests := []struct {
		field string
		want  any
	}{
		{"code", "TEST_CODE"},
		{"status", float64(http.StatusBadRequest)},
		{"title", "Bad Request"},
		{"detail", "bad input"},
	}
	for _, tc := range tests {
		if m[tc.field] != tc.want {
			t.Errorf("field %q: want %v, got %v", tc.field, tc.want, m[tc.field])
		}
	}
}

func TestAPIError_ProblemJSON_OmitEmpty(t *testing.T) {

	e := httpkit.Error("X", http.StatusBadRequest, "d")
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["type"]; ok {
		t.Errorf("type should be omitted when empty, got %v", m["type"])
	}
	if _, ok := m["instance"]; ok {
		t.Errorf("instance should be omitted when empty")
	}
}

func TestAPIError_WithDefaults(t *testing.T) {
	e := httpkit.APIError{Status: http.StatusBadRequest, Detail: "d"}
	got := e.WithDefaults()
	if got.Type != "about:blank" {
		t.Errorf("Type: want about:blank, got %q", got.Type)
	}
	if got.Title != "Bad Request" {
		t.Errorf("Title: want Bad Request, got %q", got.Title)
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if m["type"] != "about:blank" {
		t.Errorf("serialised type: want about:blank, got %v", m["type"])
	}

	custom := httpkit.APIError{Type: "https://errors.example/foo", Status: 400}.WithDefaults()
	if custom.Type != "https://errors.example/foo" {
		t.Errorf("explicit Type overwritten: %q", custom.Type)
	}
}

func TestProblemContentType(t *testing.T) {
	if httpkit.ProblemContentType != "application/problem+json" {
		t.Errorf("unexpected: %s", httpkit.ProblemContentType)
	}
}

func TestAPIError_GetStatus(t *testing.T) {
	tests := []struct {
		name string
		fn   func() *httpkit.APIError
		code int
	}{
		{"BadRequest", func() *httpkit.APIError { return httpkit.BadRequest("c", "d") }, http.StatusBadRequest},
		{"Unauthorized", func() *httpkit.APIError { return httpkit.Unauthorized("c", "d") }, http.StatusUnauthorized},
		{"Forbidden", func() *httpkit.APIError { return httpkit.Forbidden("c", "d") }, http.StatusForbidden},
		{"NotFound", func() *httpkit.APIError { return httpkit.NotFound("c", "d") }, http.StatusNotFound},
		{"Conflict", func() *httpkit.APIError { return httpkit.Conflict("c", "d") }, http.StatusConflict},
		{"Internal", func() *httpkit.APIError { return httpkit.Internal("c", "d") }, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.fn()
			if e.GetStatus() != tc.code {
				t.Errorf("want %d, got %d", tc.code, e.GetStatus())
			}
		})
	}
}

var errSentinel = errors.New("sentinel error")

type typedErr struct{ msg string }

func (e *typedErr) Error() string { return e.msg }

func TestMapErr(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if httpkit.MapErr(nil) != nil {
			t.Error("expected nil")
		}
	})

	t.Run("sentinel match", func(t *testing.T) {
		mapped := httpkit.MapErr(errSentinel, httpkit.ErrorMapping{
			Match:  func(err error) bool { return errors.Is(err, errSentinel) },
			Code:   "SENTINEL",
			Status: http.StatusNotFound,
			Detail: "not found",
		})
		var apiErr *httpkit.APIError
		if !errors.As(mapped, &apiErr) {
			t.Fatalf("expected *APIError, got %T", mapped)
		}
		if apiErr.Code != "SENTINEL" {
			t.Errorf("unexpected code: %s", apiErr.Code)
		}
	})

	t.Run("no match returns original", func(t *testing.T) {
		orig := errors.New("other")
		result := httpkit.MapErr(orig)
		if !errors.Is(result, orig) {
			t.Error("expected original error returned")
		}
	})
}

func TestMapSentinel(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if httpkit.MapSentinel(nil, errSentinel, "C", 404, "d") != nil {
			t.Error("expected nil")
		}
	})

	t.Run("sentinel match", func(t *testing.T) {
		mapped := httpkit.MapSentinel(errSentinel, errSentinel, "SENT", http.StatusNotFound, "detail")
		var apiErr *httpkit.APIError
		if !errors.As(mapped, &apiErr) {
			t.Fatalf("expected *APIError, got %T", mapped)
		}
		if apiErr.Status != http.StatusNotFound {
			t.Errorf("unexpected status: %d", apiErr.Status)
		}
	})

	t.Run("typed error match", func(t *testing.T) {
		orig := &typedErr{msg: "typed"}
		mapped := httpkit.MapSentinel(orig, &typedErr{}, "TYPED", http.StatusBadRequest, "typed")
		var apiErr *httpkit.APIError
		if !errors.As(mapped, &apiErr) {
			t.Fatalf("expected *APIError, got %T", mapped)
		}
	})
}
