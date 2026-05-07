package handlertest

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
)

type API = humatest.TestAPI

func NewAPI(t *testing.T, register func(api huma.API)) API {
	t.Helper()
	_, api := humatest.New(t, huma.DefaultConfig("test", "0.0.1"))
	register(api)
	return api
}

type APIError struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func DecodeAPIError(t *testing.T, resp *httptest.ResponseRecorder) APIError {
	t.Helper()
	body := resp.Body.String()
	var wrapper struct {
		Errors []APIError `json:"errors"`
	}
	if err := json.Unmarshal([]byte(body), &wrapper); err == nil && len(wrapper.Errors) > 0 {
		return wrapper.Errors[0]
	}
	var direct APIError
	if err := json.Unmarshal([]byte(body), &direct); err != nil {
		t.Fatalf("decode api error: %v\nbody: %s", err, body)
	}
	return direct
}

func AssertStatus(t *testing.T, resp *httptest.ResponseRecorder, want int) {
	t.Helper()
	if resp.Code != want {
		t.Fatalf("status=%d want %d body=%s", resp.Code, want, resp.Body.String())
	}
}

func AssertAPIError(t *testing.T, resp *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	AssertStatus(t, resp, status)
	got := DecodeAPIError(t, resp)
	if got.Code != code {
		t.Fatalf("api error code=%q want %q (full=%+v)", got.Code, code, got)
	}
}
