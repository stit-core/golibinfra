package httpkit

import (
	"errors"
	"fmt"
	"net/http"
)

// APIError is an RFC 7807 (problem+json) compatible error payload.
//
// `Type`/`Title`/`Status`/`Detail`/`Instance` follow the spec; `Code` is an
// extension member for stable machine-readable identifiers used in our APIs.
type APIError struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code,omitempty"`
}

// ProblemContentType is the media type for RFC 7807 problem details responses.
const ProblemContentType = "application/problem+json"

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func (e *APIError) GetStatus() int {
	return e.Status
}

// WithDefaults fills in RFC 7807 default fields. If Type is empty, it is set
// to "about:blank" as required by the spec when no problem-specific URI is
// provided.
func (e APIError) WithDefaults() APIError {
	if e.Type == "" {
		e.Type = "about:blank"
	}
	if e.Title == "" && e.Status != 0 {
		e.Title = http.StatusText(e.Status)
	}
	return e
}

func Error(code string, status int, detail string) *APIError {
	return &APIError{
		Code:   code,
		Status: status,
		Title:  http.StatusText(status),
		Detail: detail,
	}
}

func BadRequest(code, detail string) *APIError {
	return Error(code, http.StatusBadRequest, detail)
}

func Unauthorized(code, detail string) *APIError {
	return Error(code, http.StatusUnauthorized, detail)
}

func Forbidden(code, detail string) *APIError {
	return Error(code, http.StatusForbidden, detail)
}

func NotFound(code, detail string) *APIError {
	return Error(code, http.StatusNotFound, detail)
}

func Conflict(code, detail string) *APIError {
	return Error(code, http.StatusConflict, detail)
}

func Internal(code, detail string) *APIError {
	return Error(code, http.StatusInternalServerError, detail)
}

type ErrorMapping struct {
	Match  func(error) bool
	Code   string
	Status int
	Detail string
}

func MapErr(err error, mappings ...ErrorMapping) error {
	if err == nil {
		return nil
	}
	for _, m := range mappings {
		if m.Match(err) {
			return Error(m.Code, m.Status, m.Detail)
		}
	}
	return err
}

func MapSentinel[E error](err error, target E, code string, status int, detail string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, target) {
		return Error(code, status, detail)
	}
	var typed E
	if errors.As(err, &typed) {
		return Error(code, status, detail)
	}
	return err
}
