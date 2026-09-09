package httpkit_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

var errSentinel = errors.New("sentinel")

type typedError struct{ msg string }

func (e *typedError) Error() string { return e.msg }

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
	for _, absent := range []string{`"type"`, `"instance"`} {
		if strings.Contains(s, absent) {
			t.Errorf("json %s must omit empty %s", s, absent)
		}
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

func TestMapErr(t *testing.T) {
	if httpkit.MapErr(nil) != nil {
		t.Error("nil must stay nil")
	}
	mapped := httpkit.MapErr(fmt.Errorf("wrap: %w", errSentinel), httpkit.ErrorMapping{
		Match:  func(err error) bool { return errors.Is(err, errSentinel) },
		Code:   "SENTINEL",
		Status: http.StatusNotFound,
		Detail: "not found",
	})
	var apiErr *httpkit.APIError
	if !errors.As(mapped, &apiErr) || apiErr.Code != "SENTINEL" {
		t.Fatalf("expected mapped APIError, got %v", mapped)
	}
	orig := errors.New("other")
	if got := httpkit.MapErr(orig); !errors.Is(got, orig) {
		t.Error("unmatched error must be returned as is")
	}
}

func TestMapIs(t *testing.T) {
	if httpkit.MapIs(nil, errSentinel, "C", 404, "d") != nil {
		t.Error("nil must stay nil")
	}
	mapped := httpkit.MapIs(fmt.Errorf("repo: %w", errSentinel), errSentinel, "SENT", http.StatusNotFound, "detail")
	var apiErr *httpkit.APIError
	if !errors.As(mapped, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("expected 404 APIError, got %v", mapped)
	}

	// Регрессия: посторонняя ошибка (например, упавшая БД) не должна стать 404.
	dbDown := errors.New("connection refused")
	if got := httpkit.MapIs(dbDown, errSentinel, "SENT", http.StatusNotFound, "detail"); !errors.Is(got, dbDown) {
		t.Fatalf("unrelated error must pass through, got %v", got)
	}
}

func TestMapAs(t *testing.T) {
	orig := fmt.Errorf("svc: %w", &typedError{msg: "typed"})
	mapped := httpkit.MapAs[*typedError](orig, "TYPED", http.StatusBadRequest, "typed")
	var apiErr *httpkit.APIError
	if !errors.As(mapped, &apiErr) || apiErr.Code != "TYPED" {
		t.Fatalf("expected APIError, got %v", mapped)
	}
	other := errors.New("other")
	if got := httpkit.MapAs[*typedError](other, "TYPED", http.StatusBadRequest, "typed"); !errors.Is(got, other) {
		t.Fatalf("unrelated error must pass through, got %v", got)
	}
	if httpkit.MapAs[*typedError](nil, "TYPED", http.StatusBadRequest, "typed") != nil {
		t.Error("nil must stay nil")
	}
}

func TestMapAs_ErrorInterfaceForbidden(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MapAs[error] must panic: it would match every error")
		}
	}()
	_ = httpkit.MapAs[error](errors.New("x"), "C", 500, "d")
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
