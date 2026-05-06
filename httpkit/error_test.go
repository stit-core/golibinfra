package httpkit_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.stit.tech/akadmin/golibinfra/httpkit"
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
