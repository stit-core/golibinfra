package natskit_test

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

// shared — один NATS с JetStream на весь пакет: контейнер на тест давал бы
// десятки стартов вместо одного, а изоляцию дают уникальные имена стримов.
var shared struct {
	url string
	err error
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	if !testing.Short() {
		stop := startSharedNATS()
		defer stop()
	}
	return m.Run()
}

func startSharedNATS() func() {
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testkit.NATSImage,
			ExposedPorts: []string{"4222/tcp"},
			Cmd:          []string{"-js"},
			WaitingFor: wait.ForAll(
				wait.ForLog("Server is ready"),
				wait.ForListeningPort("4222/tcp"),
			).WithStartupTimeoutDefault(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		shared.err = err
		return func() { _ = testcontainers.TerminateContainer(c) }
	}
	host, err := c.Host(ctx)
	if err != nil {
		shared.err = err
		return func() { _ = testcontainers.TerminateContainer(c) }
	}
	port, err := c.MappedPort(ctx, "4222")
	if err != nil {
		shared.err = err
		return func() { _ = testcontainers.TerminateContainer(c) }
	}
	shared.url = fmt.Sprintf("nats://%s:%s", host, port.Port())
	return func() { _ = testcontainers.TerminateContainer(c) }
}

// natsURL — URL общего сервера; под -short тест пропускается, без Docker падает.
func natsURL(t *testing.T) string {
	t.Helper()
	testkit.RequireDocker(t)
	if shared.err != nil {
		t.Fatalf("shared NATS is unavailable: %v", shared.err)
	}
	return shared.url
}

// nameFor — уникальные на тест имя стрима и префикс subject'ов: сервер общий,
// поэтому стрим соседнего теста не должен ловить эти сообщения.
func nameFor(t *testing.T) string {
	t.Helper()
	return strings.NewReplacer("/", "_", "-", "_", " ", "_", ".", "_").Replace(t.Name())
}

// subj — subject внутри стрима теста (`<TestName>.created`).
func subj(t *testing.T, suffix string) string {
	t.Helper()
	return nameFor(t) + "." + suffix
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

func newStreamBus(t *testing.T, cfg natskit.Config) natskit.StreamBus {
	t.Helper()
	sb, err := natskit.NewStreamBus(connect(t, natsURL(t)), logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	return sb
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

// eventually опрашивает cond до дедлайна — детерминированная замена sleep'у
// перед позитивным ассертом.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition %q not met in %s", what, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type logEntry struct {
	level string
	msg   string
	kv    []any
}

// text — запись целиком: тест ищет в ней и сообщение, и значения атрибутов.
func (e logEntry) text() string {
	return fmt.Sprintf("%s %s %v", e.level, e.msg, e.kv)
}

// memLogger собирает записи, чтобы тест проверял предупреждения drain/timeout и
// содержимое атрибутов.
type memLogger struct {
	mu      sync.Mutex
	base    []any
	entries []logEntry
}

func (l *memLogger) record(level, msg string, kv []any) {
	l.mu.Lock()
	l.entries = append(l.entries, logEntry{level: level, msg: msg, kv: append(append([]any{}, l.base...), kv...)})
	l.mu.Unlock()
}

func (l *memLogger) Debug(_ context.Context, msg string, kv ...any) { l.record("debug", msg, kv) }
func (l *memLogger) Info(_ context.Context, msg string, kv ...any)  { l.record("info", msg, kv) }
func (l *memLogger) Warn(_ context.Context, msg string, kv ...any)  { l.record("warn", msg, kv) }
func (l *memLogger) Error(_ context.Context, msg string, kv ...any) { l.record("error", msg, kv) }

func (l *memLogger) With(kv ...any) logkit.Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.base = append(l.base, kv...)
	return l
}

func (l *memLogger) Enabled(context.Context, slog.Level) bool { return true }

func (l *memLogger) has(level, msg string) bool {
	return l.find(level, msg) != nil
}

func (l *memLogger) find(level, msg string) *logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.entries {
		if l.entries[i].level == level && l.entries[i].msg == msg {
			return &l.entries[i]
		}
	}
	return nil
}

// contains — есть ли текст (например, пароль) хоть в одной записи целиком.
func (l *memLogger) contains(text string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if strings.Contains(e.text(), text) {
			return true
		}
	}
	return false
}

func (l *memLogger) dump() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.entries))
	for i, e := range l.entries {
		out[i] = e.text()
	}
	return out
}

// blackholeProxy — TCP-прокси до NATS; при drop ответы сервера отбрасываются,
// имитируя потерянный PubAck; disconnect рвёт установленные соединения.
type blackholeProxy struct {
	ln   net.Listener
	drop atomic.Bool

	mu    sync.Mutex
	conns []net.Conn
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

func (p *blackholeProxy) track(c net.Conn) {
	p.mu.Lock()
	p.conns = append(p.conns, c)
	p.mu.Unlock()
}

// stop закрывает listener и рвёт соединения: переподключиться клиенту некуда.
func (p *blackholeProxy) stop() {
	_ = p.ln.Close()
	p.disconnect()
}

// disconnect рвёт все активные соединения — клиент уходит в reconnect.
func (p *blackholeProxy) disconnect() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (p *blackholeProxy) serve(client net.Conn, target string) {
	defer client.Close()
	server, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer server.Close()
	p.track(client)
	p.track(server)
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
