package logkit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/log"
)

const otelScope = "git.stit.tech/stit-core/golibinfra/logkit"

type otelHandler struct {
	logger log.Logger
	level  slog.Leveler
	attrs  []log.KeyValue
	group  *otelGroup
}

// otelGroup — открытая WithGroup-группа; next — объемлющая.
type otelGroup struct {
	name  string
	attrs []log.KeyValue
	next  *otelGroup
}

// NewOTelHandler — мост slog → OTel Logs API: уровень, msg, атрибуты и группы
// маппятся в log.Record; trace/span id SDK берёт из ctx сам.
func NewOTelHandler(lp log.LoggerProvider, level slog.Leveler) slog.Handler {
	return &otelHandler{logger: lp.Logger(otelScope), level: level}
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
	rec.SetBody(log.StringValue(r.Message))
	rec.AddAttributes(h.attrs...)

	kvs := make([]log.KeyValue, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		if kv, ok := convertAttr(a); ok {
			kvs = append(kvs, kv)
		}
		return true
	})
	for g := h.group; g != nil; g = g.next {
		kvs = append(append(make([]log.KeyValue, 0, len(g.attrs)+len(kvs)), g.attrs...), kvs...)
		if len(kvs) == 0 {
			continue
		}
		kvs = []log.KeyValue{log.Map(g.name, kvs...)}
	}
	rec.AddAttributes(kvs...)
	h.logger.Emit(ctx, rec)
	return nil
}

func (h *otelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	kvs := make([]log.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		if kv, ok := convertAttr(a); ok {
			kvs = append(kvs, kv)
		}
	}
	out := *h
	if h.group == nil {
		out.attrs = append(append(make([]log.KeyValue, 0, len(h.attrs)+len(kvs)), h.attrs...), kvs...)
		return &out
	}
	g := *h.group
	g.attrs = append(append(make([]log.KeyValue, 0, len(g.attrs)+len(kvs)), g.attrs...), kvs...)
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

func convertAttr(a slog.Attr) (log.KeyValue, bool) {
	if a.Equal(slog.Attr{}) {
		return log.KeyValue{}, false
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		in := v.Group()
		if len(in) == 0 {
			return log.KeyValue{}, false
		}
		kvs := make([]log.KeyValue, 0, len(in))
		for _, g := range in {
			if kv, ok := convertAttr(g); ok {
				kvs = append(kvs, kv)
			}
		}
		if a.Key == "" {
			return log.KeyValue{}, false
		}
		return log.Map(a.Key, kvs...), true
	}
	return log.KeyValue{Key: a.Key, Value: convertValue(v)}, true
}

func convertValue(v slog.Value) log.Value {
	switch v.Kind() {
	case slog.KindString:
		return log.StringValue(v.String())
	case slog.KindInt64:
		return log.Int64Value(v.Int64())
	case slog.KindUint64:
		u := v.Uint64()
		if u > math.MaxInt64 {
			return log.StringValue(strconv.FormatUint(u, 10))
		}
		return log.Int64Value(int64(u)) //nolint:gosec // диапазон проверен выше
	case slog.KindFloat64:
		return log.Float64Value(v.Float64())
	case slog.KindBool:
		return log.BoolValue(v.Bool())
	case slog.KindDuration:
		return log.Int64Value(v.Duration().Nanoseconds())
	case slog.KindTime:
		return log.StringValue(v.Time().Format(time.RFC3339Nano))
	case slog.KindAny:
		return convertAny(v.Any())
	default:
		return log.StringValue(v.String())
	}
}

// convertAny держит OTel-представление совпадающим с JSON-sink'ом: error —
// строка, []byte — bytes, прочее — JSON-строка.
func convertAny(x any) log.Value {
	switch t := x.(type) {
	case nil:
		return log.Value{}
	case error:
		if _, isMarshaler := x.(json.Marshaler); !isMarshaler {
			return log.StringValue(t.Error())
		}
	case json.RawMessage:
		return log.StringValue(string(t))
	case []byte:
		return log.BytesValue(t)
	}
	if b, err := json.Marshal(x); err == nil {
		return log.StringValue(string(b))
	}
	return log.StringValue(fmt.Sprintf("%+v", x))
}
