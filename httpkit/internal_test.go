package httpkit

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// noUnwrapContext прячет Unwrap адаптера: huma.Context стороннего адаптера,
// из которого нельзя достать *http.Request. Встраивается под именем алиаса,
// иначе поле Context затенило бы одноимённый метод интерфейса.
type humaContext = huma.Context

type noUnwrapContext struct{ humaContext }

func TestRequestFrom(t *testing.T) {
	op := &huma.Operation{Method: http.MethodPost, Path: "/orders"}
	req := httptest.NewRequest(http.MethodPost, "/orders?dry=1", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer alice")
	req.RemoteAddr = "192.0.2.10:4444"
	hc := humatest.NewContext(op, req, httptest.NewRecorder())

	type key struct{}
	withValue := huma.WithContext(hc, context.WithValue(req.Context(), key{}, "v"))

	got := requestFrom(withValue)
	if got.Header.Get("Authorization") != "Bearer alice" {
		t.Errorf("adapter request lost headers: %v", got.Header)
	}
	if got.Context().Value(key{}) != "v" {
		t.Error("request must carry the current huma context")
	}

	synthetic := requestFrom(noUnwrapContext{humaContext: withValue})
	if synthetic.Method != http.MethodPost || synthetic.URL.Path != "/orders" {
		t.Errorf("synthetic request: %s %s", synthetic.Method, synthetic.URL)
	}
	if synthetic.URL.RawQuery != "dry=1" {
		t.Errorf("synthetic query: %q", synthetic.URL.RawQuery)
	}
	if synthetic.Header.Get("Authorization") != "Bearer alice" {
		t.Errorf("synthetic headers: %v", synthetic.Header)
	}
	if synthetic.RemoteAddr != "192.0.2.10:4444" || synthetic.Context().Value(key{}) != "v" {
		t.Errorf("synthetic request lost request data: %s", synthetic.RemoteAddr)
	}
}

// Регрессия: ошибки net/http (TLS handshake, Accept) не должны уходить в
// stderr мимо единственного sink'а сервиса.
func TestServer_ErrorLogGoesToLogkit(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(validConfig(), log)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if s.srv.ErrorLog == nil {
		t.Fatal("http.Server.ErrorLog must be wired to logkit")
	}
	s.srv.ErrorLog.Printf("http: TLS handshake error from 10.0.0.1")
	if !strings.Contains(buf.String(), "TLS handshake error") {
		t.Fatalf("net/http error must reach logkit: %s", buf.String())
	}
	bridge := logkitHandler{log: log}
	if !bridge.Enabled(context.Background(), 0) {
		t.Error("handler must delegate Enabled to the logger")
	}
	if h := bridge.WithAttrs(nil).WithGroup("g"); h == nil {
		t.Error("WithAttrs/WithGroup must stay usable")
	}
}

func TestCodeForStatus(t *testing.T) {
	cases := map[int]string{
		http.StatusBadRequest:            "BAD_REQUEST",
		http.StatusNotFound:              "NOT_FOUND",
		http.StatusMethodNotAllowed:      "METHOD_NOT_ALLOWED",
		http.StatusRequestEntityTooLarge: "BODY_TOO_LARGE",
		http.StatusTeapot:                "IM_A_TEAPOT",
		http.StatusUnprocessableEntity:   "VALIDATION_FAILED",
		http.StatusTooManyRequests:       "RATE_LIMITED",
		http.StatusInternalServerError:   "INTERNAL",
		http.StatusNotImplemented:        "NOT_IMPLEMENTED",
		http.StatusBadGateway:            "BAD_GATEWAY",
		http.StatusServiceUnavailable:    "UNAVAILABLE",
		http.StatusGatewayTimeout:        "GATEWAY_TIMEOUT",
		0:                                "ERROR",
	}
	for status, want := range cases {
		if got := codeForStatus(status); got != want {
			t.Errorf("codeForStatus(%d) = %q, want %q", status, got, want)
		}
	}
}

type retryError time.Duration

func (r retryError) Error() string             { return "locked" }
func (r retryError) RetryAfter() time.Duration { return time.Duration(r) }

func TestAPIError_RetryAfter(t *testing.T) {
	cases := []struct {
		name string
		e    *APIError
		want string
	}{
		{"429 без причины — секунда", Error("RATE_LIMITED", http.StatusTooManyRequests, ""), "1"},
		{"секунды вверх", &APIError{Status: http.StatusTooManyRequests, cause: retryError(2100 * time.Millisecond)}, "3"},
		{"меньше секунды — секунда", &APIError{Status: http.StatusTooManyRequests, cause: retryError(time.Millisecond)}, "1"},
		{"в глубине цепочки", &APIError{Status: http.StatusServiceUnavailable, cause: fmt.Errorf("db: %w", retryError(time.Minute))}, "60"},
		{"503 без причины — без заголовка", Unavailable("UNAVAILABLE", ""), ""},
		{"срок истёк — без заголовка", &APIError{Status: http.StatusServiceUnavailable, cause: retryError(-time.Second)}, ""},
	}
	for _, c := range cases {
		if got := c.e.retryAfter(); got != c.want {
			t.Errorf("%s: retryAfter = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMaxBody_Disabled(t *testing.T) {
	var got int
	h := MaxBody(MaxBodyConfig{MaxBytes: 0})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		got = n
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 32))))
	if rec.Code != http.StatusOK || got != 32 {
		t.Fatalf("disabled limit must pass the body through: %d %d", rec.Code, got)
	}
}

func TestNewConfig_Errors(t *testing.T) {
	t.Setenv("HTTP_READ_TIMEOUT", "not-a-duration")
	if _, err := NewConfig(); err == nil {
		t.Fatal("unparsable duration must fail NewConfig")
	}
	t.Setenv("HTTP_READ_TIMEOUT", "15s")
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "0s")
	if _, err := NewConfig(); err == nil {
		t.Fatal("invalid config must fail NewConfig")
	}
}

// Регрессия: упавший listener — не «работающий» сервис. Ошибка Serve обязана
// погасить приложение через fx.Shutdowner с ненулевым кодом выхода.
func TestModule_DeadListenerShutsDownApp(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}

	var srv *Server
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		fx.Provide(func() logkit.Logger { return log }),
		Module,
		fx.Populate(&srv),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = app.Stop(stopCtx)
	})

	done := app.Wait()
	srv.mu.Lock()
	ln := srv.ln
	srv.mu.Unlock()
	_ = ln.Close()

	select {
	case sig := <-done:
		if sig.ExitCode != 1 {
			t.Fatalf("exit code = %d, want 1", sig.ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a dead listener must shut the app down")
	}
	if !strings.Contains(buf.String(), "http server stopped") {
		t.Fatalf("the terminal error must reach the log: %s", buf.String())
	}
}
