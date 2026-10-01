package logkit

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ctxExtractor дописывает в dst атрибуты, вычисленные из ctx на момент записи.
type ctxExtractor func(ctx context.Context, dst []slog.Attr) []slog.Attr

// ctxHandler держит ctx-атрибуты на верхнем уровне записи: при открытых
// WithGroup они добавляются к корневому handler'у, поверх которого реплеится
// цепочка WithAttrs/WithGroup.
type ctxHandler struct {
	root    slog.Handler
	cur     slog.Handler
	ops     []func(slog.Handler) slog.Handler
	extract ctxExtractor
}

func newCtxHandler(inner slog.Handler, extract ctxExtractor) *ctxHandler {
	return &ctxHandler{root: inner, cur: inner, extract: extract}
}

func (h *ctxHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.cur.Enabled(ctx, l)
}

func (h *ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	extra := h.extract(ctx, nil)
	if len(extra) == 0 {
		return h.cur.Handle(ctx, r)
	}
	if len(h.ops) == 0 {
		r.AddAttrs(extra...)
		return h.cur.Handle(ctx, r)
	}
	inner := h.root.WithAttrs(extra)
	for _, op := range h.ops {
		inner = op(inner)
	}
	return inner.Handle(ctx, r)
}

func (h *ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.withOp(
		h.cur.WithAttrs(attrs),
		func(inner slog.Handler) slog.Handler { return inner.WithAttrs(attrs) },
	)
}

func (h *ctxHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.withOp(
		h.cur.WithGroup(name),
		func(inner slog.Handler) slog.Handler { return inner.WithGroup(name) },
	)
}

func (h *ctxHandler) withOp(cur slog.Handler, op func(slog.Handler) slog.Handler) slog.Handler {
	ops := make([]func(slog.Handler) slog.Handler, len(h.ops)+1)
	copy(ops, h.ops)
	ops[len(h.ops)] = op
	return &ctxHandler{root: h.root, cur: cur, ops: ops, extract: h.extract}
}

// traceCtxAttrs пишет trace_id/span_id только в JSON-sink: OTel-sink несёт их
// отдельными полями записи.
func traceCtxAttrs(ctx context.Context, dst []slog.Attr) []slog.Attr {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return dst
	}
	return append(dst,
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	)
}

// metricHandler считает эмитированные записи: Handle вызывается уже после
// Enabled, поэтому отфильтрованные по уровню вызовы не стоят ничего.
type metricHandler struct {
	slog.Handler
	messages metric.Int64Counter
}

func (h *metricHandler) Handle(ctx context.Context, r slog.Record) error {
	h.messages.Add(ctx, 1, levelMeasurement(r.Level))
	return h.Handler.Handle(ctx, r)
}

func (h *metricHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &metricHandler{Handler: h.Handler.WithAttrs(attrs), messages: h.messages}
}

func (h *metricHandler) WithGroup(name string) slog.Handler {
	return &metricHandler{Handler: h.Handler.WithGroup(name), messages: h.messages}
}

type fanoutHandler struct{ sinks []slog.Handler }

func newFanout(sinks ...slog.Handler) slog.Handler { return &fanoutHandler{sinks: sinks} }

func (h *fanoutHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, s := range h.sinks {
		if s.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, s := range h.sinks {
		if !s.Enabled(ctx, r.Level) {
			continue
		}
		// Clone: sink может дописать attrs в свою копию, back-массив общий.
		if err := s.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(h.sinks))
	for i, s := range h.sinks {
		out[i] = s.WithAttrs(attrs)
	}
	return &fanoutHandler{sinks: out}
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(h.sinks))
	for i, s := range h.sinks {
		out[i] = s.WithGroup(name)
	}
	return &fanoutHandler{sinks: out}
}
