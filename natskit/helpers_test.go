package natskit_test

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

const natsImage = "nats:2.14.6-alpine"

// startJetStreamNATS поднимает NATS с JetStream (`-js`); Core-тестам он тоже подходит.
func startJetStreamNATS(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        natsImage,
		Cmd:          []string{"-js"},
		ExposedPorts: []string{"4222/tcp"},
		WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start jetstream nats: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	port, err := c.MappedPort(ctx, "4222")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	return "nats://" + host + ":" + port.Port()
}

func connect(t *testing.T, url string) *natskit.Conn {
	t.Helper()
	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	t.Cleanup(func() { nc.Close() })
	return natskit.ExportConn(nc)
}

func jsConfig(streams ...string) natskit.Config {
	return natskit.Config{
		JetStream: natskit.JetStreamConfig{
			Enabled:       true,
			StreamNames:   streams,
			StreamManaged: true,
			Retention:     "limits",
			MaxAge:        time.Hour,
			MaxInFlight:   32,
			AckWait:       5 * time.Second,
		},
	}
}

func clientConfig(url string) natskit.Config {
	return natskit.Config{
		URL:            url,
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  100 * time.Millisecond,
		MaxReconnects:  5,
		DrainTimeout:   5 * time.Second,
	}
}

func recv[T any](t *testing.T, ch <-chan T, timeout time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for %s", what)
		var zero T
		return zero
	}
}

func expectSilence[T any](t *testing.T, ch <-chan T, d time.Duration, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %s: %v", what, v)
	case <-time.After(d):
	}
}

type logEntry struct {
	level string
	msg   string
}

// memLogger собирает записи, чтобы тест проверял предупреждения drain/timeout.
type memLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *memLogger) record(level, msg string) {
	l.mu.Lock()
	l.entries = append(l.entries, logEntry{level: level, msg: msg})
	l.mu.Unlock()
}

func (l *memLogger) Debug(_ context.Context, msg string, _ ...any) { l.record("debug", msg) }
func (l *memLogger) Info(_ context.Context, msg string, _ ...any)  { l.record("info", msg) }
func (l *memLogger) Warn(_ context.Context, msg string, _ ...any)  { l.record("warn", msg) }
func (l *memLogger) Error(_ context.Context, msg string, _ ...any) { l.record("error", msg) }
func (l *memLogger) With(...any) logkit.Logger                     { return l }

func (l *memLogger) has(level, msg string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.level == level && e.msg == msg {
			return true
		}
	}
	return false
}

// blackholeProxy — TCP-прокси до NATS; при drop ответы сервера отбрасываются,
// имитируя потерянный PubAck.
type blackholeProxy struct {
	ln   net.Listener
	drop atomic.Bool
}

func startBlackholeProxy(t *testing.T, target string) *blackholeProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &blackholeProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(client, target)
		}
	}()
	return p
}

func (p *blackholeProxy) addr() string { return p.ln.Addr().String() }

func (p *blackholeProxy) serve(client net.Conn, target string) {
	defer client.Close()
	server, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer server.Close()
	go func() { _, _ = io.Copy(server, client) }()
	buf := make([]byte, 32*1024)
	for {
		n, err := server.Read(buf)
		if n > 0 && !p.drop.Load() {
			if _, werr := client.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
