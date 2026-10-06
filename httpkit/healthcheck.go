package httpkit

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// healthcheckTimeout: /health отвечает без проб зависимостей — дольше отвечает только зависший процесс.
const healthcheckTimeout = 3 * time.Second

// Healthcheck — подкоманда проверки здоровья бинаря сервиса (`api healthcheck`) для образа
// без shell и curl: GET /health адреса HTTP_ADDR. 0 — сервис ответил 200, 1 — нет; причина —
// ERROR `healthcheck failed` в stdout.
func Healthcheck(ctx context.Context) int {
	cfg, err := NewConfig()
	if err == nil {
		err = CheckHealth(ctx, cfg.Addr)
	}
	if err == nil {
		return 0
	}
	if log, lerr := logkit.New(logkit.Config{}); lerr == nil {
		log.Error(ctx, "healthcheck failed", "err", err)
	}
	return 1
}

// CheckHealth — GET /health сервиса, слушающего addr (HTTP_ADDR), напрямую, мимо прокси окружения;
// адрес без хоста или с хостом «все интерфейсы» (`0.0.0.0`, `[::]`) — loopback. nil — ответ 200
// не дольше 3 секунд.
func CheckHealth(ctx context.Context, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("httpkit: healthcheck: HTTP_ADDR %q: %w", addr, err)
	}
	if ip, perr := netip.ParseAddr(host); host == "" || perr == nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("httpkit: healthcheck: %w", err)
	}
	// Транспорт без Proxy: HTTP_PROXY окружения увёл бы проверку своего процесса на прокси.
	client := &http.Client{Timeout: healthcheckTimeout, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("httpkit: healthcheck: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("httpkit: healthcheck: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("httpkit: healthcheck: GET %s: %s", url, resp.Status)
	}
	return nil
}
