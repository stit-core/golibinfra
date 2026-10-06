package natskit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

const testPassword = "s3cretpass"

// Пароль из NATS_URL не должен попадать в лог ни при разрыве, ни при
// переподключении: logkit режет по именам атрибутов, ключ `url` он не знает.
func TestConn_ReconnectLogsRedactedURL(t *testing.T) {
	url := natsURL(t)
	proxy := startBlackholeProxy(t, strings.TrimPrefix(url, "nats://"))

	log := &memLogger{}
	cfg := clientConfig("nats://user:" + testPassword + "@" + proxy.addr())
	cfg.ReconnectWait = 50 * time.Millisecond
	cfg.MaxReconnects = -1
	conn, err := natskit.NewConn(cfg, log)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	t.Cleanup(conn.Close)

	proxy.disconnect()
	eventually(t, 10*time.Second, "reconnect logged", func() bool {
		return log.has("info", "natskit: reconnected")
	})

	if log.contains(testPassword) {
		t.Fatalf("password leaked into logs: %v", log.dump())
	}
	if entry := log.find("info", "natskit: reconnected"); entry == nil || !strings.Contains(entry.text(), "xxxxx@") {
		t.Fatalf("reconnect entry has no redacted url: %v", log.dump())
	}
}

// Исчерпание reconnect'ов — не тишина: error в лог и отличимая ошибка пробы.
func TestConn_ReconnectsExhaustedIsLoggedAndUnhealthy(t *testing.T) {
	url := natsURL(t)
	proxy := startBlackholeProxy(t, strings.TrimPrefix(url, "nats://"))

	log := &memLogger{}
	cfg := clientConfig("nats://user:" + testPassword + "@" + proxy.addr())
	cfg.ReconnectWait = 50 * time.Millisecond
	cfg.MaxReconnects = 1
	conn, err := natskit.NewConn(cfg, log)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	t.Cleanup(conn.Close)

	proxy.stop()
	eventually(t, 15*time.Second, "closed connection logged", func() bool {
		return log.has("error", "natskit: connection closed, reconnects exhausted")
	})
	if log.contains(testPassword) {
		t.Fatalf("password leaked into logs: %v", log.dump())
	}
	eventually(t, 5*time.Second, "connection reported closed", func() bool {
		return errors.Is(conn.Healthy(), natskit.ErrConnectionClosed)
	})
}

func TestClient_ProbeReflectsConnectionState(t *testing.T) {
	client, err := natskit.New("probe", clientConfig(natsURL(t)), logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	probe := natskit.Probe(client)
	if probe.Name != "natskit:probe" {
		t.Fatalf("probe name = %q, want natskit:probe — like the instance fx.Module", probe.Name)
	}
	defaultClient, err := natskit.New("", clientConfig(natsURL(t)), logkit.Nop())
	if err != nil {
		t.Fatalf("New default: %v", err)
	}
	t.Cleanup(defaultClient.Close)
	if name := natskit.Probe(defaultClient).Name; name != "natskit" {
		t.Fatalf("default instance probe name = %q, want natskit", name)
	}
	if err := probe.Run(context.Background()); err != nil {
		t.Fatalf("probe on a live connection = %v, want nil", err)
	}

	client.Close()
	err = probe.Run(context.Background())
	if !errors.Is(err, natskit.ErrConnectionClosed) {
		t.Fatalf("probe after Close = %v, want ErrConnectionClosed", err)
	}
}

// Невалидный URL отвергается до подключения — с редактированным userinfo в ошибке.
func TestNewConn_RejectsInvalidConfig(t *testing.T) {
	cfg := clientConfig("http://user:" + testPassword + "@localhost:4222")
	_, err := natskit.NewConn(cfg, logkit.Nop())
	if !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("NewConn err = %v, want ErrConfig", err)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Fatalf("password leaked into error: %v", err)
	}
}

// Файлы аутентификации проверяются на старте: битый nkey-seed — ошибка конфига,
// а не сбой первой публикации.
func TestNewConn_AuthFiles(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.nk")
	if err := os.WriteFile(seed, []byte("not a seed"), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	creds := filepath.Join(dir, "user.creds")
	if err := os.WriteFile(creds, []byte("not credentials"), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	cfg := clientConfig(natsURL(t))
	cfg.NKeySeedFile = seed
	if _, err := natskit.NewConn(cfg, logkit.Nop()); !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("NewConn with a broken nkey seed = %v, want ErrConfig", err)
	}

	cfg = clientConfig(natsURL(t))
	cfg.CredsFile = creds
	if _, err := natskit.NewConn(cfg, logkit.Nop()); err == nil {
		t.Fatal("NewConn with broken credentials = nil, want connect error")
	}
}
