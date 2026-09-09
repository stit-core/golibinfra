package logtest_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

func TestCaptureRecordsLevelsAndFields(t *testing.T) {
	rec := logtest.Capture()
	ctx := context.Background()

	rec.Debug(ctx, "debug msg", "n", 1)
	rec.Info(ctx, "info msg", "method", "/svc/M")
	rec.Warn(ctx, "warn msg", "error", "boom")
	rec.Error(ctx, "error msg")

	cases := []struct {
		level slog.Level
		msg   string
	}{
		{slog.LevelDebug, "debug msg"},
		{slog.LevelInfo, "info msg"},
		{slog.LevelWarn, "warn msg"},
		{slog.LevelError, "error msg"},
	}
	for _, tc := range cases {
		if !rec.Has(tc.level, tc.msg) {
			t.Errorf("Has(%v, %q) = false, entries: %+v", tc.level, tc.msg, rec.Entries())
		}
	}
	if e, ok := rec.Find(slog.LevelInfo, "info msg"); !ok || e.Str("method") != "/svc/M" {
		t.Fatalf("info entry = %+v ok=%v", e, ok)
	}
	if e, _ := rec.Find(slog.LevelDebug, "debug msg"); e.Str("n") != "1" {
		t.Fatalf("non-string value must be formatted, got %q", e.Str("n"))
	}
	if e, _ := rec.Find(slog.LevelError, "error msg"); e.Str("absent") != "" {
		t.Fatal("absent field must read as empty string")
	}
	if _, ok := rec.Find(slog.LevelWarn, "no such message"); ok {
		t.Fatal("Find must not match a message that was never logged")
	}
	if got := len(rec.Entries()); got != 4 {
		t.Fatalf("Entries() = %d, want 4", got)
	}
}

func TestFindReturnsLastMatchAndFindFuncFilters(t *testing.T) {
	rec := logtest.Capture()
	ctx := context.Background()
	rec.Info(ctx, "grpc call", "method", "/svc/A", "code", "OK")
	rec.Info(ctx, "grpc call", "method", "/svc/B", "code", "OK")

	if e, _ := rec.Find(slog.LevelInfo, "grpc call"); e.Str("method") != "/svc/B" {
		t.Fatalf("Find must return the last match, got %q", e.Str("method"))
	}
	e, ok := rec.FindFunc(func(e logtest.Entry) bool { return e.Str("method") == "/svc/A" })
	if !ok || e.Str("code") != "OK" {
		t.Fatalf("FindFunc = %+v ok=%v", e, ok)
	}
	if v, ok := e.Value("code"); !ok || v != "OK" {
		t.Fatalf("Value(code) = %v ok=%v", v, ok)
	}
}

func TestWithSharesStorage(t *testing.T) {
	rec := logtest.Capture()
	child := rec.With("component", "worker")
	child.Info(context.Background(), "started", "id", 7)

	e, ok := rec.Find(slog.LevelInfo, "started")
	if !ok {
		t.Fatal("child entries must be visible through the parent recorder")
	}
	if e.Str("component") != "worker" || e.Str("id") != "7" {
		t.Fatalf("entry = %+v, want With-attrs merged with call kv", e)
	}
	child.With("nested", true).Warn(context.Background(), "deep")
	if e, ok := rec.Find(slog.LevelWarn, "deep"); !ok || e.Str("component") != "worker" || e.Str("nested") != "true" {
		t.Fatalf("grandchild entry = %+v ok=%v, want same storage and merged attrs", e, ok)
	}
}

func TestCaptureAtDropsBelowMinLevel(t *testing.T) {
	rec := logtest.CaptureAt(slog.LevelWarn)
	ctx := context.Background()
	if rec.Enabled(ctx, slog.LevelInfo) {
		t.Fatal("Enabled(Info) must be false at min level Warn")
	}
	if !rec.Enabled(ctx, slog.LevelWarn) || !rec.Enabled(ctx, slog.LevelError) {
		t.Fatal("Enabled must be true from the min level up")
	}
	rec.Info(ctx, "dropped")
	rec.Warn(ctx, "kept")
	if rec.Has(slog.LevelInfo, "dropped") {
		t.Fatal("entry below the min level must not be recorded")
	}
	if !rec.Has(slog.LevelWarn, "kept") {
		t.Fatal("entry at the min level must be recorded")
	}
}

func TestResetAndConcurrentUse(t *testing.T) {
	rec := logtest.Capture()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			rec.With("worker", i).Info(context.Background(), "tick", "i", i)
		})
	}
	wg.Wait()
	if got := len(rec.Entries()); got != 8 {
		t.Fatalf("Entries() = %d, want 8", got)
	}
	rec.Reset()
	if got := len(rec.Entries()); got != 0 {
		t.Fatalf("Entries() after Reset = %d, want 0", got)
	}
}
