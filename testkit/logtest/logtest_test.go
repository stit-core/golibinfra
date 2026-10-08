package logtest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/logkit"
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
	if e, _ := rec.Find(slog.LevelError, "error msg"); e.Str("absent") != "" || len(e.KV) != 0 {
		t.Fatalf("a record without attributes: %+v, want empty KV and \"\" for an absent field", e)
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
	rec.With("k", "v").Info(ctx, "dropped by the child")
	if got := rec.Entries(); len(got) != 1 || got[0].Level != slog.LevelWarn || got[0].Msg != "kept" {
		t.Fatalf("entries = %+v, want only the record at the min level", got)
	}
	if !logtest.Capture().Enabled(ctx, slog.LevelDebug) {
		t.Fatal("Capture must accept every level of logkit.Logger")
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

type resolved struct{}

func (resolved) LogValue() slog.Value { return slog.StringValue("resolved") }

// Регрессия KTST-05: запись фейка — запись боевого логгера: slog.Attr, нечётный хвост, LogValuer,
// группа и error доходят до теста такими, какими их пишет slog.
func TestRecorder_ArgumentsAsLogkitWritesThem(t *testing.T) {
	ctx := context.Background()
	rec := logtest.Capture()
	rec.Info(ctx, "attr", slog.String("a", "1"), "user", "alice")
	rec.Info(ctx, "odd tail", "user", "alice", "dangling")
	rec.Info(ctx, "valuer", "v", resolved{})
	rec.Info(ctx, "group", slog.Group("req", "id", "r1"))
	rec.Info(ctx, "error", "error", errors.New("boom"))
	rec.Info(ctx, "number", "bytes", 1048576, "ratio", 0.5)

	want := []map[string]any{
		{"a": "1", "user": "alice"},
		{"user": "alice", "!BADKEY": "dangling"},
		{"v": "resolved"},
		{"req": map[string]any{"id": "r1"}},
		{"error": "boom"},
		{"bytes": json.Number("1048576"), "ratio": json.Number("0.5")},
	}
	got := rec.Entries()
	if len(got) != len(want) {
		t.Fatalf("entries = %+v, want %d", got, len(want))
	}
	for i, e := range got {
		if !reflect.DeepEqual(e.KV, want[i]) {
			t.Errorf("%q: KV = %#v, want %#v", e.Msg, e.KV, want[i])
		}
	}
	if e, _ := rec.Find(slog.LevelInfo, "number"); e.Str("bytes") != "1048576" {
		t.Errorf("Str of a number = %q, want its digits as in the log line", e.Str("bytes"))
	}
}

// Маски — те же, что у боевого логгера: тест «секрет не попал в лог» проверяет итог цепочки, а
// значение под ключом-секретом, в сообщении и у With-атрибутов видит заменённым.
func TestRecorder_RedactsAsLogkit(t *testing.T) {
	ctx := context.Background()
	rec := logtest.Capture()
	rec.With("refresh_token", "r-1").Info(ctx, "mail to alice@example.com", "password", "hunter2", "user_id", "u1")

	e := rec.Entries()[0]
	if e.Msg != "mail to [REDACTED]" {
		t.Errorf("Msg = %q, want the address masked", e.Msg)
	}
	for _, key := range []string{"password", "refresh_token"} {
		if e.Str(key) != "[REDACTED]" {
			t.Errorf("%s = %q, want it masked", key, e.Str(key))
		}
	}
	if e.Str("user_id") != "u1" {
		t.Errorf("user_id = %q: a plain field must stay", e.Str("user_id"))
	}
}

// ctx-атрибуты logkit доходят до записи фейка, как до строки боевого лога.
func TestRecorder_ContextAttributes(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0x01}, SpanID: trace.SpanID{0x02}, TraceFlags: trace.FlagsSampled,
	})
	rec := logtest.Capture()
	rec.Info(trace.ContextWithSpanContext(context.Background(), sc), "traced")
	rec.Info(context.Background(), "plain")

	traced, _ := rec.Find(slog.LevelInfo, "traced")
	if traced.Str("trace_id") != sc.TraceID().String() || traced.Str("span_id") != sc.SpanID().String() {
		t.Fatalf("traced entry = %+v, want trace_id and span_id of the context", traced)
	}
	if plain, _ := rec.Find(slog.LevelInfo, "plain"); len(plain.KV) != 0 {
		t.Fatalf("entry without a span = %+v, want no context attributes", plain)
	}
}

// Атрибут с именем поля записи (msg, level, time) идёт в KV и уровень с сообщением не затирает.
func TestRecorder_AttributeNamedLikeRecordField(t *testing.T) {
	rec := logtest.Capture()
	rec.Warn(context.Background(), "real message", "msg", "attr", "level", "high", "time", "noon")

	e, ok := rec.Find(slog.LevelWarn, "real message")
	if !ok {
		t.Fatalf("entries = %+v, want the record under its own level and message", rec.Entries())
	}
	if want := map[string]any{"msg": "attr", "level": "high", "time": "noon"}; !reflect.DeepEqual(e.KV, want) {
		t.Fatalf("KV = %#v, want %#v", e.KV, want)
	}
}

// Паритет с боевым логгером на одном вызове: запись фейка — та же JSON-строка, что logkit.New
// пишет в stdout.
func TestRecorder_SameRecordAsLogkitNew(t *testing.T) {
	ctx := context.Background()
	type payload struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	calls := map[string][]any{
		"attr and pair":   {slog.String("password", "hunter2"), "user", "alice"},
		"odd tail":        {"user", "alice", "orphan"},
		"non-string key":  {42, "x", "user", "alice"},
		"group":           {slog.Group("req", "id", "r1", "cookie", "c")},
		"struct":          {"payload", payload{Name: "n", Token: "t"}},
		"valuer":          {"v", resolved{}},
		"duration, time":  {"took", 1500 * time.Millisecond, "at", time.Unix(1_700_000_000, 0).UTC()},
		"nil and bool":    {"none", nil, "ok", true},
		"bytes with json": {"body", []byte(`{"secret":"s","n":1}`)},
	}
	for name, kv := range calls {
		var buf bytes.Buffer
		prod, err := logkit.New(logkit.Config{Level: "debug"}, logkit.WithWriter(&buf))
		if err != nil {
			t.Fatal(err)
		}
		prod.Info(ctx, "m", kv...)
		want := map[string]any{}
		dec := json.NewDecoder(&buf)
		dec.UseNumber()
		if err := dec.Decode(&want); err != nil {
			t.Fatalf("%s: decode %q: %v", name, buf.String(), err)
		}
		if want["level"] != "INFO" || want["msg"] != "m" {
			t.Fatalf("%s: logkit record = %v", name, want)
		}
		for _, own := range []string{"time", "level", "msg"} {
			delete(want, own)
		}

		rec := logtest.Capture()
		rec.Info(ctx, "m", kv...)
		got := rec.Entries()
		if len(got) != 1 || got[0].Level != slog.LevelInfo || got[0].Msg != "m" || !reflect.DeepEqual(got[0].KV, want) {
			t.Errorf("%s: logtest = %#v, logkit wrote %#v", name, got, want)
		}
	}
}
