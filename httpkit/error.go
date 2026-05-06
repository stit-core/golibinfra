package httpkit

import (
	"errors"
	"fmt"
	"net/http"
)

type APIError struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func (e *APIError) GetStatus() int {
	return e.Status
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
