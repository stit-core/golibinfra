package httpkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
)

// APIError — RFC 7807-совместимая ошибка API; huma сериализует её со статусом Status.
type APIError struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code,omitempty"`
}

const ProblemContentType = "application/problem+json"

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

// writeAPIError — ответ из chi-middleware, где huma ещё недоступна.
func writeAPIError(w http.ResponseWriter, e *APIError) {
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(e)
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

// MapIs переводит ошибку, совпадающую с target по errors.Is, в APIError;
// остальные возвращает как есть.
func MapIs(err, target error, code string, status int, detail string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, target) {
		return Error(code, status, detail)
	}
	return err
}

// MapAs переводит ошибку, в цепочке которой есть значение типа T (errors.As),
// в APIError. T = error запрещён: он совпал бы с любой ошибкой.
func MapAs[T error](err error, code string, status int, detail string) error {
	if reflect.TypeFor[T]() == reflect.TypeFor[error]() {
		panic("httpkit: MapAs[error] matches every error; use MapIs or a concrete type")
	}
	if err == nil {
		return nil
	}
	var target T
	if errors.As(err, &target) {
		return Error(code, status, detail)
	}
	return err
}
