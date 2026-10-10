package httpkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// syncBuffer — буфер лога, который сервер пишет из своих горутин, а тест читает.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines — JSON-строки лога.
func (b *syncBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	raw := bytes.Split(bytes.TrimSpace(b.buf.Bytes()), []byte("\n"))
	out := make([]map[string]any, 0, len(raw))
	for _, line := range raw {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
		out = append(out, rec)
	}
	return out
}

// downStore — IdempotencyStore, который всегда недоступен.
type downStore struct{}

var errStoreDown = errors.New("redis: connection refused")

func (downStore) Reserve(context.Context, string, time.Duration) (string, bool, error) {
	return "", false, errStoreDown
}
func (downStore) Get(context.Context, string) ([]byte, bool, error) { return nil, false, errStoreDown }
func (downStore) Set(context.Context, string, string, []byte, time.Duration) (bool, error) {
	return false, errStoreDown
}
func (downStore) Release(context.Context, string, string) error { return errStoreDown }

// retryError — доменная ошибка, которая знает, когда повторить запрос.
type retryError time.Duration

func (r retryError) Error() string             { return "locked" }
func (r retryError) RetryAfter() time.Duration { return time.Duration(r) }

type signupInput struct {
	Body struct {
		Email    string `json:"email" format:"email"`
		Password string `json:"password" minLength:"12"`
	}
}

// contractRoutes — по маршруту на класс ошибки: сопоставленная, несопоставленная, panic,
// 429 хендлера, 503 с RetryAfter() в причине, валидация и операция под Security.
func contractRoutes(api huma.API) {
	public := func(op *huma.Operation) { *op = httpkit.Public(*op) }
	huma.Get(api, "/mapped", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, httpkit.NotFound("ITEM_NOT_FOUND", "item not found")
	}, public)
	huma.Get(api, "/unmapped", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, errors.New("pq: dial tcp 10.0.0.5:5432 password=s3cret")
	}, public)
	huma.Get(api, "/panic", func(context.Context, *struct{}) (*struct{}, error) {
		panic("boom: password=s3cret")
	}, public)
	huma.Get(api, "/slow-down", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, huma.Error429TooManyRequests("slow down")
	}, public)
	huma.Get(api, "/maintenance", func(context.Context, *struct{}) (*struct{}, error) {
		return nil, huma.Error503ServiceUnavailable("db is down: password=s3cret", fmt.Errorf("pool: %w", retryError(30*time.Second)))
	}, public)
	huma.Register(api, httpkit.Public(huma.Operation{OperationID: "signup", Method: http.MethodPost, Path: "/signup"}),
		func(context.Context, *signupInput) (*struct{}, error) { return nil, nil }) // тело ответа не нужно
	huma.Register(api, huma.Operation{
		OperationID: "me", Method: http.MethodGet, Path: "/me",
		Security: []map[string][]string{{"Bearer": {}}},
	}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil }) // тело ответа не нужно
	huma.Register(api, huma.Operation{
		OperationID: "create-order", Method: http.MethodPost, Path: "/orders",
		Security: []map[string][]string{{"Bearer": {}}},
	}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil }) // тело ответа не нужно
}

type contractCase struct {
	name, method, path, body string
	headers                  []string
	status                   int
	code                     string
	retryAfter               string // "" — заголовка нет
	logged                   bool   // строка причины (не access-log) с тем же request_id
	check                    func(t *testing.T, resp *http.Response, e contractBody)
}

type contractBody struct {
	Title     string               `json:"title"`
	Status    int                  `json:"status"`
	Detail    *string              `json:"detail"`
	Code      string               `json:"code"`
	RequestID string               `json:"request_id"`
	Errors    []httpkit.FieldError `json:"errors"`
}

// TestErrorContract: любой ответ-ошибка сервера — problem+json с code и request_id, равным
// X-Request-ID и полю лога; 422 перечисляет поля без эха значения; 5xx без текста причины,
// а причина — строкой в логгере графа; 429 и ошибка с RetryAfter() в причине несут Retry-After.
func TestErrorContract(t *testing.T) {
	logs := &syncBuffer{}
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(logs), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTP_MAX_BODY_BYTES", "256")
	t.Setenv("HTTP_IDEMPOTENCY_FAIL_CLOSED", "true")
	extract := func(r *http.Request) (context.Context, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer ok":
			return httpkit.ContextWithSubject(r.Context(), "user-1"), nil
		case "Bearer backend-down":
			return nil, fmt.Errorf("jwks: %w", httpkit.ErrAuthUnavailable)
		}
		return nil, errors.New("token signature mismatch")
	}
	srv := startApp(t,
		fx.Decorate(func(logkit.Logger) logkit.Logger { return log }),
		httpkit.ProvideSecurityScheme(func() httpkit.SecuritySchemeEntry {
			return httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"}, Extract: extract}
		}),
		fx.Provide(func() httpkit.IdempotencyStore { return downStore{} }),
		fx.Invoke(contractRoutes),
	)

	noDetail := func(t *testing.T, _ *http.Response, e contractBody) {
		t.Helper()
		if e.Detail != nil {
			t.Errorf("5xx must not carry a detail: %q", *e.Detail)
		}
	}
	cases := []contractCase{
		{name: "сопоставленная ошибка хендлера", method: http.MethodGet, path: "/mapped", status: http.StatusNotFound, code: "ITEM_NOT_FOUND"},
		{name: "несопоставленная ошибка хендлера", method: http.MethodGet, path: "/unmapped", status: http.StatusInternalServerError, code: "INTERNAL", logged: true, check: noDetail},
		{name: "panic", method: http.MethodGet, path: "/panic", status: http.StatusInternalServerError, code: "INTERNAL", logged: true, check: noDetail},
		{name: "429 хендлера без RetryAfter", method: http.MethodGet, path: "/slow-down", status: http.StatusTooManyRequests, code: "RATE_LIMITED", retryAfter: "1"},
		{name: "503 с RetryAfter в причине", method: http.MethodGet, path: "/maintenance", status: http.StatusServiceUnavailable, code: "UNAVAILABLE", retryAfter: "30", logged: true, check: noDetail},
		{
			name: "валидация", method: http.MethodPost, path: "/signup", body: `{"email":"not-an-email","password":"hunter2"}`,
			status: http.StatusUnprocessableEntity, code: "VALIDATION_FAILED",
			check: func(t *testing.T, _ *http.Response, e contractBody) {
				t.Helper()
				fields := map[string]httpkit.FieldError{}
				for _, fe := range e.Errors {
					fields[fe.Location] = fe
				}
				for _, loc := range []string{"body.email", "body.password"} {
					if fe, ok := fields[loc]; !ok || fe.Code != "INVALID" || fe.Message == "" {
						t.Errorf("422 must list %s with code INVALID and a message: %+v", loc, e.Errors)
					}
				}
			},
		},
		{
			name: "битый JSON", method: http.MethodPost, path: "/signup", body: `{"email":"a@b.c","password":"hunter2"`,
			status: http.StatusBadRequest, code: "BAD_REQUEST",
			check: func(t *testing.T, _ *http.Response, e contractBody) {
				t.Helper()
				if len(e.Errors) != 1 || e.Errors[0].Location != "body" || e.Errors[0].Code != "INVALID" {
					t.Errorf("malformed body must be one body error: %+v", e.Errors)
				}
			},
		},
		{name: "лимит тела", method: http.MethodPost, path: "/signup", body: `{"email":"` + strings.Repeat("a", 300) + `"}`, status: http.StatusRequestEntityTooLarge, code: "BODY_TOO_LARGE"},
		{name: "auth", method: http.MethodGet, path: "/me", status: http.StatusUnauthorized, code: "UNAUTHORIZED"},
		{
			name: "auth backend недоступен", method: http.MethodGet, path: "/me", headers: []string{"Authorization: Bearer backend-down"},
			status: http.StatusServiceUnavailable, code: "AUTH_UNAVAILABLE", logged: true, check: noDetail,
		},
		{
			name: "store идемпотентности недоступен", method: http.MethodPost, path: "/orders",
			headers: []string{"Authorization: Bearer ok", "Idempotency-Key: k-1"},
			status:  http.StatusServiceUnavailable, code: "IDEMPOTENCY_UNAVAILABLE", logged: true, check: noDetail,
		},
		{name: "неизвестный маршрут", method: http.MethodGet, path: "/nowhere", status: http.StatusNotFound, code: "NOT_FOUND"},
		{
			name: "метод", method: http.MethodDelete, path: "/mapped", status: http.StatusMethodNotAllowed, code: "METHOD_NOT_ALLOWED",
			check: func(t *testing.T, resp *http.Response, _ contractBody) {
				t.Helper()
				if allow := resp.Header.Values("Allow"); !slices.Equal(allow, []string{http.MethodGet, http.MethodHead}) {
					t.Errorf("405 must list the route methods in Allow (HEAD — with GET): %v", allow)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := do(t, srv, c.method, c.path, strings.NewReader(c.body), append([]string{"Content-Type: application/json"}, c.headers...)...)
			assertProblem(t, resp, c, logs)
		})
	}

	t.Run("лимит запросов", func(t *testing.T) {
		t.Setenv("HTTP_RATE_LIMIT_RPS", "1")
		t.Setenv("HTTP_RATE_LIMIT_BURST", "1")
		limited := startApp(t, fx.Decorate(func(logkit.Logger) logkit.Logger { return log }))
		_ = get(t, limited, "/health")
		resp := get(t, limited, "/health")
		assertProblem(t, resp, contractCase{status: http.StatusTooManyRequests, code: "RATE_LIMITED", retryAfter: "1"}, logs)
	})
}

func assertProblem(t *testing.T, resp *http.Response, c contractCase, logs *syncBuffer) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != c.status {
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, c.status, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != httpkit.ProblemContentType {
		t.Errorf("content-type = %q, want %q", ct, httpkit.ProblemContentType)
	}
	var e contractBody
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("body is not JSON: %s", raw)
	}
	if e.Code != c.code || e.Status != c.status || e.Title != http.StatusText(c.status) {
		t.Errorf("body = %s, want code %s and status %d", raw, c.code, c.status)
	}
	id := resp.Header.Get(httpkit.HeaderRequestID)
	if id == "" || e.RequestID != id {
		t.Errorf("request_id = %q, want X-Request-ID %q", e.RequestID, id)
	}
	if got := resp.Header.Get("Retry-After"); got != c.retryAfter {
		t.Errorf("Retry-After = %q, want %q", got, c.retryAfter)
	}
	for _, secret := range []string{"s3cret", "hunter2", "10.0.0.5", "signature mismatch"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("the body leaks %q: %s", secret, raw)
		}
	}
	if c.check != nil {
		c.check(t, resp, e)
	}

	var access, cause bool
	for _, rec := range logs.lines(t) {
		if rec["request_id"] != id {
			continue
		}
		if rec["msg"] == "http" {
			access = true
		} else {
			cause = true
		}
	}
	if !access {
		t.Errorf("no access-log line with request_id %s", id)
	}
	if cause != c.logged {
		t.Errorf("cause line with request_id %s: %v, want %v", id, cause, c.logged)
	}
}
