package logkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
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

func (e *memExporter) only(t *testing.T) sdklog.Record {
	t.Helper()
	recs := e.records()
	if len(recs) != 1 {
		t.Fatalf("otel records = %d, want 1", len(recs))
	}
	return recs[0]
}

func attrsOf(r sdklog.Record) map[string]attribute.Value {
	out := map[string]attribute.Value{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		out[string(kv.Key)] = kv.Value
		return true
	})
	return out
}

func newExporter(t *testing.T) (*memExporter, log.LoggerProvider) {
	t.Helper()
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	return exp, lp
}

func newOTelLogger(t *testing.T, buf *bytes.Buffer, opts ...Option) (Logger, *memExporter) {
	t.Helper()
	exp, lp := newExporter(t)
	return mustLogger(t, Config{Level: "info"}, buf, append(opts, WithOTel(lp))...), exp
}

func TestOTel_DualSink(t *testing.T) {
	var buf bytes.Buffer
	l, exp := newOTelLogger(t, &buf)

	ctx := spanCtx(t)
	sc := trace.SpanContextFromContext(ctx)
	l.With("svc", "api").Warn(ctx, "slow", "elapsed", 1500*time.Millisecond, "n", 3, "err", errors.New("boom"))

	if m := decodeLine(t, buf.Bytes()); m["msg"] != "slow" || m["svc"] != "api" {
		t.Fatalf("stdout sink: %v", m)
	}

	r := exp.only(t)
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

	r := exp.only(t)
	if r.Body().AsString() != "kept" || r.Severity() != log.SeverityInfo {
		t.Fatalf("record = %q/%v", r.Body().AsString(), r.Severity())
	}
}

func TestOTel_GroupsNest(t *testing.T) {
	exp, lp := newExporter(t)
	sl := mustSlog(t, Config{Level: "info"}, io.Discard, WithOTel(lp))
	sl.With("top", 1).WithGroup("req").With("id", "r1").WithGroup("user").Info("hi", "name", "n")

	a := attrsOf(exp.only(t))
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

// Ключи внутри группы OTel-sink отдаёт по алфавиту: так их укладывает
// attribute.MapValue. Тест закрепляет детерминизм — порядок вызова здесь
// намеренно обратный алфавитному.
func TestOTel_GroupKeysAreSorted(t *testing.T) {
	exp, lp := newExporter(t)
	sl := mustSlog(t, Config{Level: "info"}, io.Discard, WithOTel(lp))
	sl.Info("sorted", slog.Group("g", slog.String("z", "1"), slog.String("m", "2"), slog.String("a", "3")))

	g := attrsOf(exp.only(t))["g"].AsMap()
	got := make([]string, 0, len(g))
	for _, kv := range g {
		got = append(got, string(kv.Key))
	}
	if !slices.Equal(got, []string{"a", "m", "z"}) {
		t.Fatalf("group keys = %v, want sorted", got)
	}
}

// Регрессия: scope записей — тот же, что у метрик и спанов китов, и с версией
// модуля; иначе дашборды по логам и метрикам разъезжаются при переименовании.
func TestOTel_ScopeAndVersion(t *testing.T) {
	exp, lp := newExporter(t)
	sl := mustSlog(t, Config{Level: "info"}, io.Discard, WithOTel(lp))
	sl.Info("hi")

	rec := exp.only(t)
	scope := rec.InstrumentationScope()
	if scope.Name != otelkit.Scope("logkit") {
		t.Errorf("scope name = %q, want %q", scope.Name, otelkit.Scope("logkit"))
	}
	if scope.Version == "" {
		t.Error("scope version must carry the golibinfra version")
	}
}

func TestOTel_InlineGroup(t *testing.T) {
	exp, lp := newExporter(t)
	sl := mustSlog(t, Config{Level: "info"}, io.Discard, WithOTel(lp))
	sl.Info("inline", slog.Group("", slog.String("a", "b"), slog.Int("n", 1)), slog.String("z", "z"))

	a := attrsOf(exp.only(t))
	if a["a"].AsString() != "b" || a["n"].AsInt64() != 1 || a["z"].AsString() != "z" {
		t.Fatalf("inline group must be flattened onto the record: %v", a)
	}
	if _, ok := a[""]; ok {
		t.Errorf("empty-key map must not be emitted: %v", a)
	}
}

func TestOTel_EmptyGroupDropped(t *testing.T) {
	exp, lp := newExporter(t)
	sl := mustSlog(t, Config{Level: "info"}, io.Discard, WithOTel(lp))
	sl.Info("empty", slog.Group("g"), slog.String("k", "v"))

	a := attrsOf(exp.only(t))
	if _, ok := a["g"]; ok {
		t.Errorf("group without attrs must be dropped: %v", a)
	}
	if a["k"].AsString() != "v" {
		t.Errorf("k = %v", a["k"])
	}
}

func TestOTel_CtxAttrsStayTopLevelUnderGroup(t *testing.T) {
	ctx := requestIDAttr(t, "request_id")
	exp, lp := newExporter(t)
	sl := mustSlog(t, Config{Level: "info"}, io.Discard, WithOTel(lp))
	sl.WithGroup("req").With("id", "r1").InfoContext(ctx, "grouped", "k", "v")

	a := attrsOf(exp.only(t))
	if a["request_id"].AsString() != "req-1" {
		t.Fatalf("request_id must be a top-level attribute: %v", a)
	}
	req := a["req"].AsMap()
	if len(req) != 2 || req[0].Key != "id" || req[1].Key != "k" {
		t.Errorf("req = %v", req)
	}
}

func TestOTel_RegisterAttrReachesBothSinks(t *testing.T) {
	ctx := requestIDAttr(t, "request_id")

	var buf bytes.Buffer
	l, exp := newOTelLogger(t, &buf)
	l.Info(ctx, "hello")

	if m := decodeLine(t, buf.Bytes()); m["request_id"] != "req-1" {
		t.Errorf("stdout sink: %v", m)
	}
	if a := attrsOf(exp.only(t)); a["request_id"].AsString() != "req-1" {
		t.Errorf("otel sink: %v", a)
	}
}

func TestOTel_ConvertValue(t *testing.T) {
	ts := time.Date(2026, 5, 14, 10, 30, 0, 0, time.UTC)
	cases := map[string]struct {
		in   slog.Value
		want func(t *testing.T, got attribute.Value)
	}{
		"string": {slog.StringValue("s"), func(t *testing.T, got attribute.Value) {
			if got.AsString() != "s" {
				t.Errorf("got %v", got)
			}
		}},
		"int": {slog.Int64Value(-7), func(t *testing.T, got attribute.Value) {
			if got.AsInt64() != -7 {
				t.Errorf("got %v", got)
			}
		}},
		"uint in range": {slog.Uint64Value(42), func(t *testing.T, got attribute.Value) {
			if got.AsInt64() != 42 {
				t.Errorf("got %v", got)
			}
		}},
		"uint overflow": {slog.Uint64Value(math.MaxUint64), func(t *testing.T, got attribute.Value) {
			if got.AsString() != "18446744073709551615" {
				t.Errorf("got %v", got)
			}
		}},
		"float": {slog.Float64Value(0.5), func(t *testing.T, got attribute.Value) {
			if got.AsFloat64() != 0.5 {
				t.Errorf("got %v", got)
			}
		}},
		"bool": {slog.BoolValue(true), func(t *testing.T, got attribute.Value) {
			if !got.AsBool() {
				t.Errorf("got %v", got)
			}
		}},
		"duration": {slog.DurationValue(1500 * time.Millisecond), func(t *testing.T, got attribute.Value) {
			if got.AsInt64() != int64(1500*time.Millisecond) {
				t.Errorf("got %v", got)
			}
		}},
		"time": {slog.TimeValue(ts), func(t *testing.T, got attribute.Value) {
			if got.AsString() != ts.Format(time.RFC3339Nano) {
				t.Errorf("got %v", got)
			}
		}},
		"nil any": {slog.AnyValue(nil), func(t *testing.T, got attribute.Value) {
			if got.Type() != attribute.EMPTY {
				t.Errorf("got %v", got)
			}
		}},
		"error": {slog.AnyValue(errors.New("boom")), func(t *testing.T, got attribute.Value) {
			if got.AsString() != "boom" {
				t.Errorf("got %v", got)
			}
		}},
		"raw message": {slog.AnyValue(json.RawMessage(`{"a":1}`)), func(t *testing.T, got attribute.Value) {
			if got.AsString() != `{"a":1}` {
				t.Errorf("got %v", got)
			}
		}},
		"bytes": {slog.AnyValue([]byte{1, 2, 3}), func(t *testing.T, got attribute.Value) {
			if !bytes.Equal(got.AsByteSlice(), []byte{1, 2, 3}) {
				t.Errorf("got %v", got)
			}
		}},
		"struct": {slog.AnyValue(struct {
			A int `json:"a"`
		}{1}), func(t *testing.T, got attribute.Value) {
			if got.AsString() != `{"a":1}` {
				t.Errorf("got %v", got)
			}
		}},
		"html in struct": {slog.AnyValue(map[string]string{"tag": "<b>&</b>"}), func(t *testing.T, got attribute.Value) {
			if got.AsString() != `{"tag":"<b>&</b>"}` {
				t.Errorf("got %v, want the same encoding as the JSON sink", got)
			}
		}},
		"unmarshalable": {slog.AnyValue(make(chan int)), func(t *testing.T, got attribute.Value) {
			if got.AsString() == "" {
				t.Errorf("got %v", got)
			}
		}},
		"logvaluer": {slog.AnyValue(valuer{}), func(t *testing.T, got attribute.Value) {
			if got.AsString() != "resolved" {
				t.Errorf("got %v", got)
			}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kvs := appendAttr(nil, slog.Attr{Key: "k", Value: tc.in})
			if len(kvs) != 1 {
				t.Fatalf("appendAttr = %v, want one KeyValue", kvs)
			}
			tc.want(t, kvs[0].Value)
		})
	}
}

type valuer struct{}

func (valuer) LogValue() slog.Value { return slog.StringValue("resolved") }

func TestOTel_AppendAttrGroupRules(t *testing.T) {
	if kvs := appendAttr(nil, slog.Attr{}); len(kvs) != 0 {
		t.Fatalf("empty attr must be dropped, got %v", kvs)
	}
	if kvs := appendAttr(nil, slog.Group("g")); len(kvs) != 0 {
		t.Fatalf("group without attrs must be dropped, got %v", kvs)
	}
	kvs := appendAttr(nil, slog.Group("", slog.String("a", "b"), slog.Int("n", 1)))
	if len(kvs) != 2 || kvs[0].Key != "a" || kvs[1].Key != "n" {
		t.Fatalf("empty-key group must be inlined, got %v", kvs)
	}
	nested := appendAttr(nil, slog.Group("g", slog.Group("inner", slog.String("a", "b"))))
	if len(nested) != 1 || nested[0].Value.AsMap()[0].Key != "inner" {
		t.Fatalf("nested group lost: %v", nested)
	}
}

func TestOTel_WithGroupEmptyNameIsNoOp(t *testing.T) {
	h := NewOTelHandler(nil, slog.LevelInfo)
	if h.WithGroup("") != h {
		t.Error(`WithGroup("") must not build a new handler`)
	}
}

func TestWithoutOTel_DropsSink(t *testing.T) {
	exp, lp := newExporter(t)
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "info"}, &buf, WithOTel(lp), WithoutOTel())
	l.Info(context.Background(), "local only")

	if len(exp.records()) != 0 {
		t.Fatalf("WithoutOTel must drop the OTel sink, got %d records", len(exp.records()))
	}
	if buf.Len() == 0 {
		t.Fatal("local sink must still write")
	}
}

func TestOTel_RedactAppliesToBothSinks(t *testing.T) {
	var buf bytes.Buffer
	l, exp := newOTelLogger(t, &buf, WithRedact(RedactConfig{}))
	l.Info(context.Background(), "mail to a@b.io", "email", "a@b.io")

	if m := decodeLine(t, buf.Bytes()); m["email"] != "[REDACTED]" || m["msg"] != "mail to [REDACTED]" {
		t.Errorf("stdout: %v", m)
	}
	r := exp.only(t)
	if a := attrsOf(r); a["email"].AsString() != "[REDACTED]" || r.Body().AsString() != "mail to [REDACTED]" {
		t.Errorf("otel: %v / %q", a, r.Body().AsString())
	}
}

func TestNewOTelHandler_NilArgs(t *testing.T) {
	h := NewOTelHandler(nil, nil)
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug must be filtered by the default info level")
	}
	if err := h.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelError, "msg", 0)); err != nil {
		t.Fatalf("nil provider must give a no-op sink, got %v", err)
	}
}

func TestSeverityMapping(t *testing.T) {
	cases := map[int]log.Severity{-12: log.SeverityTrace1, -4: log.SeverityDebug, 0: log.SeverityInfo, 4: log.SeverityWarn, 8: log.SeverityError, 40: log.SeverityFatal4}
	for lvl, want := range cases {
		if got := severity(slog.Level(lvl)); got != want {
			t.Errorf("severity(%d) = %v, want %v", lvl, got, want)
		}
	}
}
