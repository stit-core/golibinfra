package logkit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/noop"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type otelHandler struct {
	logger log.Logger
	level  slog.Leveler
	attrs  []attribute.KeyValue
	group  *otelGroup
}

// otelGroup — открытая WithGroup-группа; next — объемлющая.
type otelGroup struct {
	name  string
	attrs []attribute.KeyValue
	next  *otelGroup
}

// NewOTelHandler — мост slog → OTel Logs API: уровень, msg, атрибуты и группы
// маппятся в log.Record; trace/span id SDK берёт из ctx сам. nil-аргументы
// дают no-op sink и уровень info.
func NewOTelHandler(lp log.LoggerProvider, level slog.Leveler) slog.Handler {
	if lp == nil {
		lp = noop.NewLoggerProvider()
	}
	if level == nil {
		level = slog.LevelInfo
	}
	logger := lp.Logger(otelkit.Scope("logkit"), log.WithInstrumentationVersion(otelkit.Version()))
	return &otelHandler{logger: logger, level: level}
}

func (h *otelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	if l < h.level.Level() {
		return false
	}
	return h.logger.Enabled(ctx, log.EnabledParameters{Severity: severity(l)})
}

func (h *otelHandler) Handle(ctx context.Context, r slog.Record) error {
	var rec log.Record
	rec.SetTimestamp(r.Time)
	rec.SetObservedTimestamp(time.Now())
	rec.SetSeverity(severity(r.Level))
	rec.SetSeverityText(r.Level.String())
	rec.SetBody(attribute.StringValue(r.Message))
	rec.AddAttributes(h.attrs...)

	kvs := make([]attribute.KeyValue, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		kvs = appendAttr(kvs, a)
		return true
	})
	for g := h.group; g != nil; g = g.next {
		kvs = append(append(make([]attribute.KeyValue, 0, len(g.attrs)+len(kvs)), g.attrs...), kvs...)
		if len(kvs) == 0 {
			continue
		}
		kvs = []attribute.KeyValue{groupValue(g.name, kvs)}
	}
	rec.AddAttributes(kvs...)
	h.logger.Emit(ctx, rec)
	return nil
}

func (h *otelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	kvs := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		kvs = appendAttr(kvs, a)
	}
	out := *h
	if h.group == nil {
		out.attrs = append(append(make([]attribute.KeyValue, 0, len(h.attrs)+len(kvs)), h.attrs...), kvs...)
		return &out
	}
	g := *h.group
	g.attrs = append(append(make([]attribute.KeyValue, 0, len(g.attrs)+len(kvs)), g.attrs...), kvs...)
	out.group = &g
	return &out
}

func (h *otelHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	out := *h
	out.group = &otelGroup{name: name, next: h.group}
	return &out
}

// severity: slog.Level + 9 совпадает с OTel-шкалой (Debug 5, Info 9, Warn 13,
// Error 17); за пределами шкалы — обрезка.
func severity(l slog.Level) log.Severity {
	s := int(l) + 9
	switch {
	case s < int(log.SeverityTrace1):
		return log.SeverityTrace1
	case s > int(log.SeverityFatal4):
		return log.SeverityFatal4
	}
	return log.Severity(s)
}

// appendAttr следует контракту slog: пустой Attr отбрасывается, группа с
// пустым ключом инлайнится, группа без атрибутов отбрасывается.
func appendAttr(dst []attribute.KeyValue, a slog.Attr) []attribute.KeyValue {
	if a.Equal(slog.Attr{}) {
		return dst
	}
	v := a.Value.Resolve()
	if v.Kind() != slog.KindGroup {
		return append(dst, attribute.KeyValue{Key: attribute.Key(a.Key), Value: convertValue(v)})
	}
	in := v.Group()
	if a.Key == "" {
		for _, g := range in {
			dst = appendAttr(dst, g)
		}
		return dst
	}
	kvs := make([]attribute.KeyValue, 0, len(in))
	for _, g := range in {
		kvs = appendAttr(kvs, g)
	}
	if len(kvs) == 0 {
		return dst
	}
	return append(dst, groupValue(a.Key, kvs))
}

// groupValue укладывает группу в MAP-значение записи. attribute.MapValue
// сортирует ключи, поэтому внутри группы OTel-sink отдаёт их по алфавиту, а не
// в порядке вызова, — в отличие от локального JSON-sink'а.
func groupValue(name string, kvs []attribute.KeyValue) attribute.KeyValue {
	return attribute.KeyValue{Key: attribute.Key(name), Value: attribute.MapValue(kvs...)}
}

func convertValue(v slog.Value) attribute.Value {
	switch v.Kind() {
	case slog.KindString:
		return attribute.StringValue(v.String())
	case slog.KindInt64:
		return attribute.Int64Value(v.Int64())
	case slog.KindUint64:
		u := v.Uint64()
		if u > math.MaxInt64 {
			return attribute.StringValue(strconv.FormatUint(u, 10))
		}
		return attribute.Int64Value(int64(u))
	case slog.KindFloat64:
		return attribute.Float64Value(v.Float64())
	case slog.KindBool:
		return attribute.BoolValue(v.Bool())
	case slog.KindDuration:
		return attribute.Int64Value(v.Duration().Nanoseconds())
	case slog.KindTime:
		return attribute.StringValue(v.Time().Format(time.RFC3339Nano))
	case slog.KindAny:
		return convertAny(v.Any())
	default:
		return attribute.StringValue(v.String())
	}
}

// convertAny держит OTel-представление совпадающим с JSON-sink'ом: error —
// строка, []byte — bytes, прочее — JSON-строка.
func convertAny(x any) attribute.Value {
	switch t := x.(type) {
	case nil:
		return attribute.Value{}
	case error:
		if _, isMarshaler := x.(json.Marshaler); !isMarshaler {
			return attribute.StringValue(t.Error())
		}
	case json.RawMessage:
		return attribute.StringValue(string(t))
	case []byte:
		return attribute.ByteSliceValue(t)
	}
	if b, err := marshalJSON(x); err == nil {
		return attribute.StringValue(string(b))
	}
	return attribute.StringValue(fmt.Sprintf("%+v", x))
}
