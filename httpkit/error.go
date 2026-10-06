package httpkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/validation"
)

// Форма ошибки одна на процесс: ошибки самой huma (валидация, 5xx хендлера,
// huma.Error4xx/5xx) — тоже APIError, в том числе в humatest тестов потребителя.
func init() {
	huma.NewError = newError
	huma.NewErrorWithContext = func(_ huma.Context, status int, msg string, errs ...error) huma.StatusError {
		return newError(status, msg, errs...)
	}
}

// APIError — ошибка API в форме RFC 9457 (application/problem+json) с машинным
// `code` и `request_id`. 5xx, собранная китом, не несёт текста причины: причина
// остаётся в логе сервера.
type APIError struct {
	Title     string       `json:"title" doc:"HTTP status text"`
	Status    int          `json:"status" doc:"HTTP status code"`
	Detail    string       `json:"detail,omitempty" doc:"Human-readable explanation of this occurrence; may be absent, and a 5xx never carries its internal cause"`
	Code      string       `json:"code" doc:"Stable machine-readable error code. Operation codes are listed in the response description; generic ones include VALIDATION_FAILED, BAD_REQUEST, UNAUTHORIZED, NOT_FOUND, METHOD_NOT_ALLOWED, BODY_TOO_LARGE, RATE_LIMITED, INTERNAL, UNAVAILABLE, REQUEST_TIMEOUT"`
	RequestID string       `json:"request_id,omitempty" doc:"Request ID (X-Request-ID); the server logs carry the same ID"`
	Errors    []FieldError `json:"errors,omitempty" doc:"What is wrong in the request, field by field"`

	cause error
}

// FieldError — ошибка одного места запроса: location (`body.email`, `path.id`,
// `query.limit`, `header.<name>`), машинный code и message. Ошибки валидации
// huma получают code INVALID и значение поля не цитируют: хвост сообщений,
// которые его цитируют (форматы, ошибка UnmarshalText параметра), обрезается.
// Текст декодера тела (синтаксис JSON, UnmarshalText/UnmarshalJSON типа поля)
// huma отдаёт как есть.
type FieldError struct {
	Location string `json:"location" doc:"Where in the request: body.<field>, path.<name>, query.<name>, header.<name>"`
	Code     string `json:"code" doc:"Machine-readable code of this field error"`
	Message  string `json:"message" doc:"Human-readable message"`
}

// ProblemContentType — content-type ошибок кита (RFC 9457).
const ProblemContentType = "application/problem+json"

// codeInvalid — code ошибки поля из валидации huma.
const codeInvalid = "INVALID"

// Error возвращает `<code>: <detail>`; APIError реализует error.
func (e *APIError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
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

// retryAfterer — причина, которая знает, когда повторить запрос.
type retryAfterer interface{ RetryAfter() time.Duration }

// retryAfter — значение заголовка Retry-After в секундах (вверх): RetryAfter()
// из цепочки причины, у 429 — не меньше секунды; "" — заголовка нет.
func (e *APIError) retryAfter() string {
	var d time.Duration
	var ra retryAfterer
	if errors.As(e.cause, &ra) {
		d = ra.RetryAfter()
	}
	if d <= 0 && e.Status != http.StatusTooManyRequests {
		return ""
	}
	return strconv.FormatInt(max(1, int64(math.Ceil(d.Seconds()))), 10)
}

// forRequest — копия для ответа с request_id запроса: общий APIError (package-level
// значение) не мутируется конкурентными запросами.
func (e *APIError) forRequest(ctx context.Context) *APIError {
	out := *e
	out.RequestID = RequestIDFrom(ctx)
	return &out
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

// neutral — 5xx без текста причины: клиенту — статус и код, cause — в лог сервера.
func neutral(status int, cause error) *APIError {
	return &APIError{Title: http.StatusText(status), Status: status, Code: codeForStatus(status), cause: cause}
}

// newError — huma.NewError кита: 5xx нейтральна, 4xx несёт msg и ошибки полей
// без эха значения (в нём бывает пароль); *http.MaxBytesError в причине — 413.
func newError(status int, msg string, errs ...error) huma.StatusError {
	cause := errors.Join(errs...)
	// Тело сверх лимита MaxBody huma читает как ошибку чтения (500), а это превышение лимита.
	if IsMaxBodyError(cause) {
		return Error(codeForStatus(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge, "request body is too large")
	}
	if status >= http.StatusInternalServerError {
		if cause == nil {
			return neutral(status, errors.New(msg))
		}
		return neutral(status, fmt.Errorf("%s: %w", msg, cause))
	}
	e := Error(codeForStatus(status), status, msg)
	e.cause = cause
	for _, err := range errs {
		if err == nil {
			continue
		}
		fe := FieldError{Code: codeInvalid, Message: err.Error()}
		var d huma.ErrorDetailer
		if errors.As(err, &d) {
			detail := d.ErrorDetail()
			fe.Location, fe.Message = detail.Location, withoutEcho(detail.Message)
		}
		e.Errors = append(e.Errors, fe)
	}
	return e
}

// echoMessages — сообщения huma, которые после «: » цитируют ввод: форматы
// (адрес, URL, префикс uuid, regex, длительность) и ошибка UnmarshalText
// параметра (текст декодера: `ParseAddr("…")`).
var echoMessages = []string{
	validation.MsgExpectedRFC5322Email, validation.MsgExpectedRFC3986URI,
	validation.MsgExpectedRFC4122UUID, validation.MsgExpectedRegexp, validation.MsgExpectedDuration,
	"invalid value: %v", // parseInto huma: константы у сообщения нет
}

// withoutEcho отрезает у сообщения хвост с вводом: в поле бывает пароль.
func withoutEcho(msg string) string {
	for _, format := range echoMessages {
		if prefix, ok := strings.CutSuffix(format, "%v"); ok && strings.HasPrefix(msg, prefix) {
			return strings.TrimSuffix(prefix, ": ")
		}
	}
	return msg
}

// codeForStatus — code ошибки, собранной по статусу: свои имена кита или текст статуса.
func codeForStatus(status int) string {
	switch status {
	case http.StatusRequestEntityTooLarge:
		return "BODY_TOO_LARGE"
	case http.StatusUnprocessableEntity:
		return "VALIDATION_FAILED"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
	case http.StatusInternalServerError:
		return "INTERNAL"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	}
	if text := http.StatusText(status); text != "" {
		return strings.ToUpper(strings.NewReplacer(" ", "_", "-", "_", "'", "").Replace(text))
	}
	return "ERROR"
}

// writeAPIError — ответ-ошибка вне huma: chi-middleware и маршрутизатор.
func writeAPIError(ctx context.Context, w http.ResponseWriter, e *APIError) {
	body := e.forRequest(ctx)
	if ra := body.retryAfter(); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(body.Status)
	_ = json.NewEncoder(w).Encode(body)
}
