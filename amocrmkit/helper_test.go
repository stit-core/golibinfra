package amocrmkit

import (
	"context"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// testHTTPConfig — httpclient без ретраев и с высоким порогом breaker'а, чтобы
// тесты проверяли только логику кита.
func testHTTPConfig() httpclient.Config {
	return httpclient.Config{
		Timeout:    5 * time.Second,
		MaxRetries: 0,
		RetryWait:  time.Millisecond,
		Breaker: httpclient.BreakerConfig{
			FailureThreshold: 100,
			RecoveryTimeout:  time.Second,
		},
	}
}

// newTestClient собирает Client, направленный на httptest-сервер (srvURL) через
// HostOverride. RateRPS высокий — тесты не троттлятся (лимит-тесты задают свой).
func newTestClient(t *testing.T, srvURL string) *Client {
	t.Helper()
	c, err := New(Config{RateRPS: 1000, HostOverride: srvURL}, testHTTPConfig(), clockkit.Real{}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func testAccount() Account {
	return Account{Subdomain: "demo", TLD: "ru", Token: "test-token"}
}

type logEntry struct {
	level string
	msg   string
	kv    map[string]any
}

type captureLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *captureLogger) add(level, msg string, kv []any) {
	e := logEntry{level: level, msg: msg, kv: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			e.kv[k] = kv[i+1]
		}
	}
	l.mu.Lock()
	l.entries = append(l.entries, e)
	l.mu.Unlock()
}

func (l *captureLogger) Debug(_ context.Context, msg string, kv ...any) { l.add("debug", msg, kv) }
func (l *captureLogger) Info(_ context.Context, msg string, kv ...any)  { l.add("info", msg, kv) }
func (l *captureLogger) Warn(_ context.Context, msg string, kv ...any)  { l.add("warn", msg, kv) }
func (l *captureLogger) Error(_ context.Context, msg string, kv ...any) { l.add("error", msg, kv) }
func (l *captureLogger) With(...any) logkit.Logger                      { return l }

func (l *captureLogger) find(level, msg string) (logEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.level == level && e.msg == msg {
			return e, true
		}
	}
	return logEntry{}, false
}
