package httpkit_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func testConfig(t *testing.T, addr string) httpkit.Config {
	t.Helper()
	t.Setenv("HTTP_ADDR", addr)
	cfg, err := httpkit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	return cfg
}

func startApp(t *testing.T, probe healthkit.Probe) *httpkit.Server {
	t.Helper()
	var srv *httpkit.Server
	app := fx.New(
		fx.NopLogger,
		fx.Supply(testConfig(t, "127.0.0.1:0")),
		fx.Provide(func() logkit.Logger { return logkit.Nop() }),
		httpkit.Module,
		healthkit.Provide(func() healthkit.Probe { return probe }),
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
	return srv
}

func get(t *testing.T, srv *httpkit.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get("http://" + srv.Addr().String() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// Регрессия: пробы китов из группы `health` должны доходить до /ready.
func TestModule_ReadyReflectsProbes(t *testing.T) {
	down := healthkit.Probe(func(context.Context) error { return errors.New("postgres: connection refused") })
	srv := startApp(t, down)

	if resp := get(t, srv, "/health"); resp.StatusCode != http.StatusOK {
		t.Fatalf("/health: %d", resp.StatusCode)
	}
	resp := get(t, srv, "/ready")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/ready with failing probe: want 503, got %d", resp.StatusCode)
	}
	if resp.Header.Get(httpkit.HeaderRequestID) == "" {
		t.Error("response must carry X-Request-ID")
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("security headers must be applied")
	}

	ok := healthkit.Probe(func(context.Context) error { return nil })
	srv2 := startApp(t, ok)
	if resp := get(t, srv2, "/ready"); resp.StatusCode != http.StatusOK {
		t.Fatalf("/ready with ok probe: want 200, got %d", resp.StatusCode)
	}
}

func TestModule_ReadyDoesNotLeakProbeError(t *testing.T) {
	down := healthkit.Probe(func(context.Context) error {
		return errors.New("postgres://user:secret@10.0.0.5:5432 refused")
	})
	srv := startApp(t, down)
	resp := get(t, srv, "/ready")
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if body := string(buf[:n]); contains(body, "secret") || contains(body, "10.0.0.5") {
		t.Fatalf("probe error leaked into /ready body: %s", body)
	}
}

func TestModule_StartFailsOnBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	app := fx.New(
		fx.NopLogger,
		fx.Supply(testConfig(t, ln.Addr().String())),
		fx.Provide(func() logkit.Logger { return logkit.Nop() }),
		httpkit.Module,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err == nil {
		_ = app.Stop(ctx)
		t.Fatal("start on a busy port must fail synchronously")
	}
}

func TestConfig_Validate(t *testing.T) {
	bad := testConfig(t, ":0")
	bad.CORS = httpkit.CORSConfig{Origins: []string{"*"}, AllowCredentials: true}
	if err := bad.Validate(); !errors.Is(err, httpkit.ErrConfig) {
		t.Errorf("credentials with wildcard origin must be rejected: %v", err)
	}
	bad = testConfig(t, ":0")
	bad.ShutdownTimeout = 0
	if err := bad.Validate(); !errors.Is(err, httpkit.ErrConfig) {
		t.Errorf("zero shutdown timeout must be rejected: %v", err)
	}
	if err := testConfig(t, ":0").Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
