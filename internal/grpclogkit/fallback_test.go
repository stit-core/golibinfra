package grpclogkit

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/grpclog"
)

func TestFallbackWriters(t *testing.T) {
	cases := map[string]struct{ info, warning, error bool }{
		"":        {error: true},
		"ERROR":   {error: true},
		"error":   {error: true},
		"garbage": {error: true},
		"WARNING": {warning: true},
		"warning": {warning: true},
		"INFO":    {info: true},
		"info":    {info: true},
	}
	for severity, want := range cases {
		t.Run("severity="+severity, func(t *testing.T) {
			infoW, warningW, errorW := fallbackWriters(severity)
			for name, got := range map[string]struct {
				w      io.Writer
				stderr bool
			}{
				"info":    {infoW, want.info},
				"warning": {warningW, want.warning},
				"error":   {errorW, want.error},
			} {
				if got.stderr && got.w != os.Stderr {
					t.Errorf("%s writer = %v, want os.Stderr", name, got.w)
				}
				if !got.stderr && got.w != io.Discard {
					t.Errorf("%s writer = %v, want io.Discard", name, got.w)
				}
			}
		})
	}
}

func TestNewFallbackHonoursGrpcEnv(t *testing.T) {
	t.Setenv("GRPC_GO_LOG_SEVERITY_LEVEL", "INFO")
	t.Setenv("GRPC_GO_LOG_VERBOSITY_LEVEL", "3")

	lg := newFallback()
	if !lg.V(3) {
		t.Error("V(3) must be true with GRPC_GO_LOG_VERBOSITY_LEVEL=3")
	}
	if lg.V(4) {
		t.Error("V(4) must be false with GRPC_GO_LOG_VERBOSITY_LEVEL=3")
	}
}

// Без установленного логгера записи grpc-go уходят его дефолтным логгером:
// импорт кита не должен превращать ошибки транспорта в тишину.
func TestBridgeWithoutLoggerUsesGrpcDefault(t *testing.T) {
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	prev := fallback
	fallback = sync.OnceValue(func() grpclog.LoggerV2 {
		w := &lockedWriter{mu: &mu, w: &buf}
		return grpclog.NewLoggerV2WithVerbosity(w, w, w, 1)
	})
	t.Cleanup(func() { fallback = prev })

	b := &bridge{}
	b.Info("resolver updated")
	b.Warning("subchannel is idle")
	b.Errorf("received GoAway: %s", "too_many_pings")

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	for _, want := range []string{"resolver updated", "subchannel is idle", "received GoAway: too_many_pings"} {
		if !strings.Contains(out, want) {
			t.Errorf("grpc default logger did not receive %q, got:\n%s", want, out)
		}
	}
	if !b.V(1) || b.V(2) {
		t.Errorf("V must follow the verbosity of the default logger, V(1)=%v V(2)=%v", b.V(1), b.V(2))
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
