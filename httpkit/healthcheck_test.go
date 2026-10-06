package httpkit_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// healthServer отвечает на GET /health статусом status, на прочие пути — 404.
func healthServer(t *testing.T, status int) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// closedAddr — адрес, который никто не слушает.
func closedAddr(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.Listener.Addr().String()
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestCheckHealth(t *testing.T) {
	live := healthServer(t, http.StatusOK)
	tests := []struct {
		name    string
		addr    string
		healthy bool
	}{
		{"listener address", live, true},
		{"address without host is loopback", ":" + portOf(t, live), true},
		{"unspecified host", "0.0.0.0:" + portOf(t, live), true},
		{"health is not ok", healthServer(t, http.StatusServiceUnavailable), false},
		{"nobody listens", closedAddr(t), false},
		{"address without port", "8080", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := httpkit.CheckHealth(context.Background(), tt.addr)
			if tt.healthy != (err == nil) {
				t.Fatalf("CheckHealth(%q) = %v, want healthy=%v", tt.addr, err, tt.healthy)
			}
		})
	}
}

// Сервер Module отвечает проверке здоровья по своему адресу; код выхода подкоманды — по /health
// адреса из HTTP_ADDR. Без t.Parallel: t.Setenv.
func TestHealthcheck(t *testing.T) {
	srv := startApp(t)
	if err := httpkit.CheckHealth(context.Background(), srv.Addr().String()); err != nil {
		t.Fatalf("CheckHealth of the Module server: %v", err)
	}

	t.Setenv("HTTP_ADDR", ":"+portOf(t, srv.Addr().String()))
	if code := httpkit.Healthcheck(context.Background()); code != 0 {
		t.Fatalf("Healthcheck with a live service = %d, want 0", code)
	}
	t.Setenv("HTTP_ADDR", closedAddr(t))
	if code := httpkit.Healthcheck(context.Background()); code != 1 {
		t.Fatalf("Healthcheck with nobody listening = %d, want 1", code)
	}
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "0s")
	if code := httpkit.Healthcheck(context.Background()); code != 1 {
		t.Fatalf("Healthcheck with an invalid config = %d, want 1", code)
	}
}
