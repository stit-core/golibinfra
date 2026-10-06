package httpkit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func TestCORS_Preflight(t *testing.T) {
	cfg := httpkit.CORSConfig{
		Origins: []string{"https://app.example.com"},
		Methods: []string{"GET", "POST"},
		Headers: []string{"Authorization"},
		MaxAge:  600,
	}
	h := httpkit.CORS(cfg)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("ACAO = %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("credentials header must be absent unless enabled")
	}
}

func TestCORS_UnknownOriginNoHeaders(t *testing.T) {
	h := httpkit.CORS(httpkit.CORSConfig{Origins: []string{"https://app.example.com"}})(http.HandlerFunc(okHandler))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unknown origin must pass through without ACAO: %d %v", rec.Code, rec.Header())
	}
}

func TestCORS_WildcardAndCredentials(t *testing.T) {
	h := httpkit.CORS(httpkit.CORSConfig{Origins: []string{"*"}})(http.HandlerFunc(okHandler))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://any.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("wildcard ACAO = %q", got)
	}

	h = httpkit.CORS(httpkit.CORSConfig{Origins: []string{"https://app.example.com"}, AllowCredentials: true})(http.HandlerFunc(okHandler))
	req.Header.Set("Origin", "https://app.example.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("credentials must be allowed for a listed origin")
	}
}

func TestCORS_Disabled(t *testing.T) {
	h := httpkit.CORS(httpkit.CORSConfig{})(http.HandlerFunc(okHandler))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS should be disabled when origins empty")
	}
}

func TestRateLimit_AllowAndReject(t *testing.T) {
	// RPS=1: за время 101 запроса ведро не успевает пополниться, 101-й всегда 429.
	h := httpkit.RateLimit(httpkit.RateLimitConfig{RPS: 1, Burst: 100})(http.HandlerFunc(okHandler))
	var last *httptest.ResponseRecorder
	for range 101 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		last = httptest.NewRecorder()
		h.ServeHTTP(last, req)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("101st request: want 429, got %d", last.Code)
	}
	if last.Header().Get("Retry-After") == "" || last.Header().Get("Content-Type") != httpkit.ProblemContentType {
		t.Errorf("429 must carry Retry-After and problem+json: %v", last.Header())
	}
}

func TestRateLimit_Disabled(t *testing.T) {
	h := httpkit.RateLimit(httpkit.RateLimitConfig{RPS: 0})(http.HandlerFunc(okHandler))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200 when disabled, got %d", rec.Code)
	}
}

func TestMaxBody_OK(t *testing.T) {
	var received []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		received = b
		w.WriteHeader(http.StatusOK)
	})
	h := httpkit.MaxBody(httpkit.MaxBodyConfig{MaxBytes: 1 << 20})(handler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello")))
	if rec.Code != http.StatusOK || string(received) != "hello" {
		t.Fatalf("got %d %q", rec.Code, received)
	}
}

func TestMaxBody_TooLarge(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			if !httpkit.IsMaxBodyError(err) {
				t.Errorf("want MaxBytesError, got %v", err)
			}
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := httpkit.MaxBody(httpkit.MaxBodyConfig{MaxBytes: 4})(handler)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("way too long")))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", rec.Code)
	}
}

func TestRequestID_ValidatesClientValue(t *testing.T) {
	var seen string
	h := httpkit.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpkit.RequestIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(httpkit.HeaderRequestID, "client-42")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen != "client-42" || rec.Header().Get(httpkit.HeaderRequestID) != "client-42" {
		t.Fatalf("valid client id must be kept: ctx=%q header=%q", seen, rec.Header().Get(httpkit.HeaderRequestID))
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(httpkit.HeaderRequestID, strings.Repeat("x", 300)+"\n")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seen == "" || len(seen) > 128 || strings.Contains(seen, "\n") {
		t.Fatalf("invalid client id must be replaced, got %q", seen)
	}
}

// Поле request_id — только у записи с request-id в ctx: пустым оно в лог не попадает.
func TestRequestID_LogFieldOnlyWithID(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	log.Info(context.Background(), "without")
	log.Info(httpkit.ContextWithRequestID(context.Background(), "req-7"), "with")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], `"request_id"`) || !strings.Contains(lines[1], `"request_id":"req-7"`) {
		t.Fatalf("request_id must be logged only with an id in ctx: %q", lines)
	}
}

func TestRecovery_PanicTo500(t *testing.T) {
	h := httpkit.Recovery(logkit.Nop())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != httpkit.ProblemContentType {
		t.Fatalf("got %d %v", rec.Code, rec.Header())
	}
}

func TestRecovery_AbortHandlerPropagates(t *testing.T) {
	h := httpkit.Recovery(logkit.Nop())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		err, _ := recover().(error)
		if !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("ErrAbortHandler must propagate, got %v", err)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestChain_FlushReachesUnderlyingWriter(t *testing.T) {
	var flushed bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("w must implement http.Flusher through the chain")
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController.Flush: %v", err)
		}
		flushed = true
	})
	h := httpkit.Recovery(logkit.Nop())(httpkit.AccessLog(logkit.Nop())(httpkit.Metrics()(inner)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !flushed || !rec.Flushed {
		t.Fatalf("flush must reach the recorder: handler=%v recorder=%v", flushed, rec.Flushed)
	}
}

// Регрессия: без Expose-Headers cross-origin SPA не читает X-Request-ID,
// Location и Retry-After, которые кит сам же и ставит.
func TestCORS_ExposeHeaders(t *testing.T) {
	cfg := httpkit.CORSConfig{
		Origins:       []string{"https://app.example.com"},
		ExposeHeaders: []string{"X-Request-ID", "Location"},
	}
	h := httpkit.CORS(cfg)(http.HandlerFunc(okHandler))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "X-Request-ID, Location" {
		t.Fatalf("Access-Control-Expose-Headers = %q", got)
	}
}

// Регрессия: прямой вызов CORS() минует Config.Validate, поэтому связка
// «*» + credentials не должна отражать произвольный origin.
func TestCORS_WildcardNeverEchoesOriginWithCredentials(t *testing.T) {
	h := httpkit.CORS(httpkit.CORSConfig{Origins: []string{"*"}, AllowCredentials: true})(http.HandlerFunc(okHandler))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("ACAO = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("credentials must not be granted to a wildcard origin: %q", got)
	}
}

func TestAccessLog_QuietPathsAndDurationUnit(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	h := httpkit.AccessLog(log, "/ready")(http.HandlerFunc(okHandler))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ready", nil))
	if buf.Len() != 0 {
		t.Fatalf("successful probe must not write an Info line: %s", buf.String())
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/orders", nil))
	line := buf.String()
	if !strings.Contains(line, `"path":"/orders"`) || !strings.Contains(line, `"duration_ms":`) {
		t.Fatalf("access log line: %s", line)
	}
	if strings.Contains(line, `"dur":`) {
		t.Fatalf("duration must carry its unit in the field name: %s", line)
	}
}

func TestAccessLog_QuietPathStillLogsFailures(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	h := httpkit.AccessLog(log, "/ready")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ready", nil))
	if !strings.Contains(buf.String(), `"status":503`) {
		t.Fatalf("failing probe must stay in the log: %s", buf.String())
	}
}

func TestAccessLog_PanicStillLogged(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	h := httpkit.AccessLog(log)(httpkit.Recovery(logkit.Nop())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	if !strings.Contains(buf.String(), `"status":500`) || !strings.Contains(buf.String(), `"path":"/boom"`) {
		t.Fatalf("panic request must produce an access-log line: %s", buf.String())
	}
}
