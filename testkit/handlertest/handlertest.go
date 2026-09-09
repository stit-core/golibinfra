// Package handlertest — хелперы для тестов huma-хендлеров: in-memory API поверх
// humatest и разбор тела ошибки (плоский httpkit.APIError и huma.ErrorModel).
package handlertest

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
)

// API — huma.API с записью ответов; алиас humatest.TestAPI.
type API = humatest.TestAPI

// NewAPI поднимает in-memory huma.API и регистрирует на ней хендлеры register.
func NewAPI(t testing.TB, register func(api huma.API)) API {
	t.Helper()
	_, api := humatest.New(t, huma.DefaultConfig("test", "0.0.1"))
	register(api)
	return api
}

// APIError — тело ошибки API: поля RFC 9457 + `code` httpkit и `errors` huma.
type APIError struct {
	Type     string             `json:"type"`
	Title    string             `json:"title"`
	Status   int                `json:"status"`
	Detail   string             `json:"detail"`
	Instance string             `json:"instance"`
	Code     string             `json:"code"`
	Errors   []APIErrorLocation `json:"errors"`
}

// APIErrorLocation — элемент `errors` huma: сообщение и место в запросе.
type APIErrorLocation struct {
	Message  string `json:"message"`
	Location string `json:"location"`
	Value    any    `json:"value"`
}

// DecodeAPIError разбирает тело ответа как APIError; невалидный JSON — t.Fatalf.
func DecodeAPIError(t testing.TB, resp *httptest.ResponseRecorder) APIError {
	t.Helper()
	body := resp.Body.Bytes()
	var got APIError
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("handlertest: decode api error: %v\nbody: %s", err, body)
	}
	return got
}

// AssertStatus проверяет код ответа.
func AssertStatus(t testing.TB, resp *httptest.ResponseRecorder, want int) {
	t.Helper()
	if resp.Code != want {
		t.Fatalf("handlertest: status = %d, want %d (body: %s)", resp.Code, want, resp.Body.String())
	}
}

// AssertAPIError проверяет код ответа и поле `code` тела ошибки.
func AssertAPIError(t testing.TB, resp *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	AssertStatus(t, resp, status)
	got := DecodeAPIError(t, resp)
	if got.Code != code {
		t.Fatalf("handlertest: api error code = %q, want %q (full: %+v)", got.Code, code, got)
	}
}
