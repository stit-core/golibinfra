package logkit

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

type memExporter struct {
	mu   sync.Mutex
	recs []sdklog.Record
}

func (e *memExporter) Export(_ context.Context, recs []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range recs {
		e.recs = append(e.recs, r.Clone())
	}
	return nil
}

func (e *memExporter) Shutdown(context.Context) error   { return nil }
func (e *memExporter) ForceFlush(context.Context) error { return nil }

func (e *memExporter) records() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdklog.Record(nil), e.recs...)
}

func attrsOf(r sdklog.Record) map[string]log.Value {
	out := map[string]log.Value{}
	r.WalkAttributes(func(kv log.KeyValue) bool {
		out[kv.Key] = kv.Value
		return true
	})
	return out
}

func newOTelLogger(t *testing.T, buf *bytes.Buffer, opts ...Option) (Logger, *memExporter) {
	t.Helper()
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	l := mustLogger(t, Config{Level: "info"}, buf, append(opts, WithOTel(lp))...)
	return l, exp
}

func TestOTel_DualSink(t *testing.T) {
	var buf bytes.Buffer
	l, exp := newOTelLogger(t, &buf)

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	l.With("svc", "api").Warn(ctx, "slow", "elapsed", 1500*time.Millisecond, "n", 3, "err", errors.New("boom"))

	if m := decodeLine(t, buf.Bytes()); m["msg"] != "slow" || m["svc"] != "api" {
		t.Fatalf("stdout sink: %v", m)
	}

	recs := exp.records()
	if len(recs) != 1 {
		t.Fatalf("otel records = %d, want 1", len(recs))
	}
	r := recs[0]
	if r.Body().AsString() != "slow" || r.Severity() != log.SeverityWarn || r.SeverityText() != "WARN" {
		t.Errorf("body/severity = %q/%v/%q", r.Body().AsString(), r.Severity(), r.SeverityText())
	}
	if r.TraceID() != sc.TraceID() || r.SpanID() != sc.SpanID() {
		t.Errorf("trace/span = %s/%s, want from ctx", r.TraceID(), r.SpanID())
	}
	a := attrsOf(r)
	if a["svc"].AsString() != "api" {
		t.Errorf("With attr svc = %v", a["svc"])
	}
	if a["elapsed"].AsInt64() != int64(1500*time.Millisecond) {
		t.Errorf("elapsed = %v, want ns int", a["elapsed"])
	}
	if a["n"].AsInt64() != 3 {
		t.Errorf("n = %v", a["n"])
	}
	if a["err"].AsString() != "boom" {
		t.Errorf("err = %v", a["err"])
	}
	if _, ok := a["trace_id"]; ok {
		t.Errorf("trace_id must be a record field, not an attribute: %v", a)
	}
}

func TestOTel_LevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l, exp := newOTelLogger(t, &buf)
	l.Debug(context.Background(), "dropped")
	l.Info(context.Background(), "kept")

	recs := exp.records()
	if len(recs) != 1 || recs[0].Body().AsString() != "kept" {
		t.Fatalf("records = %d", len(recs))
	}
	if recs[0].Severity() != log.SeverityInfo {
		t.Errorf("severity = %v", recs[0].Severity())
	}
}

func TestOTel_GroupsNest(t *testing.T) {
	var buf bytes.Buffer
	l, exp := newOTelLogger(t, &buf)
	sl, err := NewSlogWithWriter(Config{Level: "info"}, &buf, WithOTel(exp.provider(t)))
	if err != nil {
		t.Fatal(err)
	}
	_ = l
	sl.With("top", 1).WithGroup("req").With("id", "r1").WithGroup("user").Info("hi", "name", "n")

	recs := exp.records()
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	a := attrsOf(recs[0])
	if a["top"].AsInt64() != 1 {
		t.Errorf("top = %v", a["top"])
	}
	req := a["req"].AsMap()
	if len(req) != 2 || req[0].Key != "id" || req[0].Value.AsString() != "r1" || req[1].Key != "user" {
		t.Fatalf("req = %v", req)
	}
	user := req[1].Value.AsMap()
	if len(user) != 1 || user[0].Key != "name" || user[0].Value.AsString() != "n" {
		t.Errorf("user = %v", user)
	}
}

func (e *memExporter) provider(t *testing.T) log.LoggerProvider {
	t.Helper()
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(e)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	return lp
}

func TestOTel_RedactAppliesToBothSinks(t *testing.T) {
	var buf bytes.Buffer
	l, exp := newOTelLogger(t, &buf, WithRedact(RedactConfig{Enabled: true}))
	l.Info(context.Background(), "mail to a@b.io", "email", "a@b.io")

	if m := decodeLine(t, buf.Bytes()); m["email"] != "[REDACTED]" || m["msg"] != "mail to [REDACTED]" {
		t.Errorf("stdout: %v", m)
	}
	recs := exp.records()
	if a := attrsOf(recs[0]); a["email"].AsString() != "[REDACTED]" || recs[0].Body().AsString() != "mail to [REDACTED]" {
		t.Errorf("otel: %v / %q", a, recs[0].Body().AsString())
	}
}

func slogLevel(l int) slog.Level { return slog.Level(l) }

func TestSeverityMapping(t *testing.T) {
	cases := map[int]log.Severity{-12: log.SeverityTrace1, -4: log.SeverityDebug, 0: log.SeverityInfo, 4: log.SeverityWarn, 8: log.SeverityError, 40: log.SeverityFatal4}
	for lvl, want := range cases {
		if got := severity(slogLevel(lvl)); got != want {
			t.Errorf("severity(%d) = %v, want %v", lvl, got, want)
		}
	}
}
