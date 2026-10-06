package httpkit_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

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
		{"unspecified IPv6 host is loopback", "[::]:" + portOf(t, live), true},
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

// Проверка своего процесса идёт напрямую: прокси окружения, который отвечает 200 на всё, не
// делает здоровым адрес, где никто не слушает. 192.0.2.1 (TEST-NET-1) — не loopback, мимо
// прокси его пускает только транспорт без Proxy. Свой процесс: net/http читает HTTP_PROXY
// один раз на процесс.
func TestCheckHealth_BypassesEnvProxy(t *testing.T) {
	const child = "HTTPKIT_CHECKHEALTH_PROXY_CHILD"
	if os.Getenv(child) == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		fmt.Print(httpkit.CheckHealth(ctx, "192.0.2.1:9"))
		return
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(proxy.Close)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCheckHealth_BypassesEnvProxy$")
	cmd.Env = append(os.Environ(), child+"=1", "HTTP_PROXY="+proxy.URL, "NO_PROXY=", "no_proxy=")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "httpkit: healthcheck:") {
		t.Fatalf("CheckHealth of a dead address through an always-200 HTTP_PROXY must fail; child: %v\n%s", err, out)
	}
}

// Зависший сервис — ошибка не позже таймаута проверки: у ctx подкоманды дедлайна нет.
func TestCheckHealth_HungServiceFails(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	done := make(chan error, 1)
	go func() { done <- httpkit.CheckHealth(context.Background(), srv.Listener.Addr().String()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("CheckHealth of a hung service = nil, want an error")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("CheckHealth of a hung service did not return within its 3s timeout")
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
