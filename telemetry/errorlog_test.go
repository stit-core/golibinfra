package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

type capture struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	handler slog.Handler
}

func newCapture() *capture { return newCaptureAt(slog.LevelDebug) }

func newCaptureAt(level slog.Level) *capture {
	c := &capture{}
	c.handler = slog.NewJSONHandler(&syncWriter{c: c}, &slog.HandlerOptions{Level: level})
	return c
}

type syncWriter struct{ c *capture }

func (w *syncWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.c.buf.Write(p)
}

func (c *capture) lines() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for l := range strings.SplitSeq(strings.TrimSpace(c.buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// Регрессия: без SetErrorHandler ошибки экспорта уходят plain-text'ом в stderr
// мимо logkit — без level, service и redact'а.
func TestInstallErrorLogging_RoutesOTelErrorsToHandler(t *testing.T) {
	restoreGlobals(t)

	c := newCapture()
	installErrorLogging(c.handler)

	otel.Handle(errors.New("traces export: connection refused"))

	lines := c.lines()
	if len(lines) != 1 {
		t.Fatalf("lines: got %d, want 1 (%v)", len(lines), lines)
	}
	if lines[0]["level"] != "ERROR" {
		t.Fatalf("level = %v, want ERROR", lines[0]["level"])
	}
	if got, _ := lines[0]["err"].(string); !strings.Contains(got, "connection refused") {
		t.Fatalf("err field = %q", got)
	}
}

func TestInstallErrorLogging_RateLimitsRepeatedText(t *testing.T) {
	restoreGlobals(t)

	c := newCapture()
	installErrorLogging(c.handler)

	same := errors.New("traces export: connection refused")
	for range 5 {
		otel.Handle(same)
	}
	otel.Handle(errors.New("metrics export: connection refused"))

	if got := len(c.lines()); got != 2 {
		t.Fatalf("lines: got %d, want 2 (one per distinct error text)", got)
	}
}

func TestInstallErrorLogging_NilHandlerIsNoOp(t *testing.T) {
	restoreGlobals(t)

	c := newCapture()
	installErrorLogging(c.handler)
	installErrorLogging(nil)

	otel.Handle(errors.New("still routed"))
	if got := len(c.lines()); got != 1 {
		t.Fatalf("nil handler must leave the installed one in place; lines = %d", got)
	}
}

// Регрессия: logr кодирует V(n) отрицательным slog-уровнем, поэтому на
// дефолтном LOG_LEVEL=info внутренние предупреждения SDK («dropped log
// records») не доходили до лога вовсе.
func TestInstallErrorLogging_RaisesSDKWarningsToWarn(t *testing.T) {
	restoreGlobals(t)

	c := newCaptureAt(slog.LevelInfo)
	installErrorLogging(c.handler)

	sdklog.NewLoggerProvider().Logger("") // provider.go: global.Warn("Invalid Logger name.")

	lines := c.lines()
	if len(lines) != 1 {
		t.Fatalf("lines: got %d, want 1 (%v)", len(lines), lines)
	}
	if lines[0]["level"] != "WARN" {
		t.Fatalf("level = %v, want WARN", lines[0]["level"])
	}
	if msg, _ := lines[0]["msg"].(string); !strings.Contains(msg, "Invalid Logger name") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestSDKLevel(t *testing.T) {
	t.Parallel()

	cases := map[slog.Level]slog.Level{
		slog.LevelError: slog.LevelError, // logr.Error
		slog.LevelInfo:  slog.LevelInfo,  // V(0)
		-1:              slog.LevelWarn,  // V(1) — global.Warn
		-3:              slog.LevelWarn,
		-4:              slog.LevelDebug, // V(4) — global.Info
		-8:              slog.LevelDebug, // V(8) — global.Debug
	}
	for in, want := range cases {
		if got := sdkLevel(in); got != want {
			t.Errorf("sdkLevel(%v) = %v, want %v", in, got, want)
		}
	}
}

// Enabled должен спрашивать нижний handler о переведённом уровне: иначе
// info-логгер отвечает «выключено» и logr молчит ещё до Handle.
func TestLogrHandler_EnabledUsesTranslatedLevel(t *testing.T) {
	t.Parallel()

	c := newCaptureAt(slog.LevelInfo)
	h := &logrHandler{Handler: c.handler}
	if !h.Enabled(context.Background(), -1) {
		t.Error("V(1) must be enabled at info: it maps to warn")
	}
	if h.Enabled(context.Background(), -4) {
		t.Error("V(4) must be filtered at info: it maps to debug")
	}
}

func TestLogrHandler_KeepsAttrsAndGroups(t *testing.T) {
	t.Parallel()

	c := newCaptureAt(slog.LevelInfo)
	log := slog.New(&logrHandler{Handler: c.handler}).With("component", "otel").WithGroup("sdk")
	log.Log(context.Background(), -1, "dropped log records", "dropped", 7)

	lines := c.lines()
	if len(lines) != 1 {
		t.Fatalf("lines: got %d, want 1 (%v)", len(lines), lines)
	}
	if lines[0]["level"] != "WARN" || lines[0]["component"] != "otel" {
		t.Fatalf("record = %v", lines[0])
	}
	group, _ := lines[0]["sdk"].(map[string]any)
	if group == nil || group["dropped"] == nil {
		t.Fatalf("WithGroup lost: %v", lines[0])
	}
}

func TestLimiter(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lim := newLimiter(time.Minute, func() time.Time { return now })

	if !lim.allow("a") {
		t.Fatal("first occurrence must pass")
	}
	if lim.allow("a") {
		t.Fatal("repeat within the window must be dropped")
	}
	if !lim.allow("b") {
		t.Fatal("a different key must pass")
	}
	now = now.Add(time.Minute)
	if !lim.allow("a") {
		t.Fatal("after the window the key must pass again")
	}
}

// Регрессия: множество текстов ошибок экспорта не ограничено — map не должен расти.
func TestLimiter_BoundsKeys(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lim := newLimiter(time.Minute, func() time.Time { return now })
	for i := range errorLogKeys * 3 {
		lim.allow(string(rune('a'+i%26)) + strings.Repeat("x", i))
	}
	lim.mu.Lock()
	defer lim.mu.Unlock()
	if len(lim.seen) > errorLogKeys {
		t.Fatalf("tracked keys: got %d, want <= %d", len(lim.seen), errorLogKeys)
	}
}

// На потолке ключей окно чистится сначала выборочно: живые ключи не должны
// терять дросселирование из-за одного всплеска новых текстов.
func TestLimiter_EvictsExpiredKeysAtCap(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lim := newLimiter(time.Minute, func() time.Time { return now })
	for i := range errorLogKeys {
		lim.allow("old-" + strconv.Itoa(i))
	}
	now = now.Add(2 * time.Minute)
	lim.allow("fresh")

	lim.mu.Lock()
	defer lim.mu.Unlock()
	if len(lim.seen) != 1 {
		t.Fatalf("tracked keys: got %d, want 1 (expired keys evicted)", len(lim.seen))
	}
	if _, ok := lim.seen["fresh"]; !ok {
		t.Fatalf("the new key must stay tracked: %v", lim.seen)
	}
}

func TestLimitHandler_ThrottlesByMessage(t *testing.T) {
	t.Parallel()

	c := newCapture()
	now := time.Now()
	h := &limitHandler{Handler: c.handler, lim: newLimiter(time.Minute, func() time.Time { return now })}
	log := slog.New(h).With("component", "otel")

	for range 3 {
		log.Error("exporter failed")
	}
	log.Error("another problem")

	if got := len(c.lines()); got != 2 {
		t.Fatalf("lines: got %d, want 2", got)
	}
	if c.lines()[0]["component"] != "otel" {
		t.Fatalf("WithAttrs must survive throttling: %v", c.lines()[0])
	}
}

func TestLimitHandler_WithGroup(t *testing.T) {
	t.Parallel()

	c := newCapture()
	h := &limitHandler{Handler: c.handler, lim: newLimiter(time.Minute, time.Now)}
	slog.New(h).WithGroup("otel").Error("boom", "code", 7)

	lines := c.lines()
	if len(lines) != 1 {
		t.Fatalf("lines: got %d, want 1", len(lines))
	}
	group, _ := lines[0]["otel"].(map[string]any)
	if group == nil || group["code"] == nil {
		t.Fatalf("WithGroup lost: %v", lines[0])
	}
}
