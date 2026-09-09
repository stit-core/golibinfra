package httpkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
)

// APIError — RFC 9457-совместимая ошибка API; huma сериализует её со статусом
// Status и content-type ProblemContentType.
type APIError struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code,omitempty"`
}

// ProblemContentType — content-type ошибок кита (RFC 9457).
const ProblemContentType = "application/problem+json"

// Error возвращает `<code>: <detail>`; APIError реализует error.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

// GetStatus реализует huma.StatusError: статус ответа берётся из ошибки.
func (e *APIError) GetStatus() int {
	return e.Status
}

// ContentType реализует huma.ContentTypeFilter: ошибка уходит как
// problem+json из любого слоя — и из huma-хендлера, и из chi-middleware.
func (e *APIError) ContentType(ct string) string {
	if ct == "application/json" {
		return ProblemContentType
	}
	return ct
}

// Error собирает APIError с указанным кодом и статусом.
func Error(code string, status int, detail string) *APIError {
	return &APIError{
		Code:   code,
		Status: status,
		Title:  http.StatusText(status),
		Detail: detail,
	}
}

// BadRequest — APIError со статусом 400.
func BadRequest(code, detail string) *APIError {
	return Error(code, http.StatusBadRequest, detail)
}

// Unauthorized — APIError со статусом 401.
func Unauthorized(code, detail string) *APIError {
	return Error(code, http.StatusUnauthorized, detail)
}

// Forbidden — APIError со статусом 403.
func Forbidden(code, detail string) *APIError {
	return Error(code, http.StatusForbidden, detail)
}

// NotFound — APIError со статусом 404.
func NotFound(code, detail string) *APIError {
	return Error(code, http.StatusNotFound, detail)
}

// Conflict — APIError со статусом 409.
func Conflict(code, detail string) *APIError {
	return Error(code, http.StatusConflict, detail)
}

// Internal — APIError со статусом 500.
func Internal(code, detail string) *APIError {
	return Error(code, http.StatusInternalServerError, detail)
}

// Unavailable — APIError со статусом 503.
func Unavailable(code, detail string) *APIError {
	return Error(code, http.StatusServiceUnavailable, detail)
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusNotImplemented:
		return "NOT_IMPLEMENTED"
	case http.StatusBadGateway:
		return "BAD_GATEWAY"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusGatewayTimeout:
		return "GATEWAY_TIMEOUT"
	default:
		return "INTERNAL"
	}
}

// writeAPIError — ответ из chi-middleware, где huma ещё недоступна.
func writeAPIError(w http.ResponseWriter, e *APIError) {
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(e)
}

// ErrorMapping — одно правило MapErr: предикат по ошибке и результирующий APIError.
type ErrorMapping struct {
	Match  func(error) bool
	Code   string
	Status int
	Detail string
}

// MapErr переводит ошибку в APIError по первому совпавшему правилу; остальные
// возвращает как есть (их подхватит error-hook кита: нейтральный 500 + лог).
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
	if _, ok := errors.AsType[T](err); ok {
		return Error(code, status, detail)
	}
	return err
}
