// Package handlertest — хелперы для тестов huma-хендлеров: in-memory API с тем же
// контрактом ошибок, что у сервера httpkit, и разбор тела ошибки httpkit.APIError.
package handlertest

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// API — huma.API с записью ответов; алиас humatest.TestAPI.
type API = humatest.TestAPI

// NewAPI поднимает in-memory huma.API сервера httpkit (request_id, Retry-After,
// 404/405 маршрутизатора, коды ErrorMap.Declare в OpenAPI) и регистрирует на нём
// хендлеры register. Middleware цепочки, кроме RequestID, в нём нет.
func NewAPI(t testing.TB, register func(api huma.API)) API {
	t.Helper()
	cfg := httpkit.Config{
		APITitle: "test", APIVersion: "0.0.1", DocsEnabled: true,
		MaxBody: httpkit.MaxBodyConfig{MaxBytes: 1 << 20},
	}
	s := httpkit.NewServer(cfg, httpkit.WithMiddleware(httpkit.RequestID()))
	api := humatest.Wrap(t, s.BindAPI())
	register(api)
	return api
}

// DecodeAPIError разбирает тело ответа как httpkit.APIError; невалидный JSON — t.Fatalf.
func DecodeAPIError(t testing.TB, resp *httptest.ResponseRecorder) httpkit.APIError {
	t.Helper()
	body := resp.Body.Bytes()
	var got httpkit.APIError
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

// AssertAPIError проверяет ответ-ошибку по контракту httpkit: статус,
// application/problem+json, поле `code` и request_id, равный X-Request-ID ответа.
func AssertAPIError(t testing.TB, resp *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	AssertStatus(t, resp, status)
	if ct := resp.Header().Get("Content-Type"); ct != httpkit.ProblemContentType {
		t.Fatalf("handlertest: content-type = %q, want %q (body: %s)", ct, httpkit.ProblemContentType, resp.Body.String())
	}
	got := DecodeAPIError(t, resp)
	if got.Code != code {
		t.Fatalf("handlertest: api error code = %q, want %q (full: %+v)", got.Code, code, got)
	}
	if id := resp.Header().Get(httpkit.HeaderRequestID); got.RequestID != id {
		t.Fatalf("handlertest: request_id = %q, want X-Request-ID %q", got.RequestID, id)
	}
}
