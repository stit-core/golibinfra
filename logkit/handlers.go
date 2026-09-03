package logkit

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// ctxAttrsHandler добавляет user-registered attrs (RegisterAttr) — до fan-out,
// чтобы оба sink'а получили, например, request_id из httpkit.
type ctxAttrsHandler struct{ slog.Handler }

func (h *ctxAttrsHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, fn := range collectAttrs() {
		if k, v, ok := fn(ctx); ok {
			r.AddAttrs(slog.Any(k, v))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h *ctxAttrsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ctxAttrsHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *ctxAttrsHandler) WithGroup(name string) slog.Handler {
	return &ctxAttrsHandler{Handler: h.Handler.WithGroup(name)}
}

// traceHandler пишет trace_id/span_id атрибутами только в JSON-sink: OTel-sink
// несёт их отдельными полями записи.
type traceHandler struct{ slog.Handler }

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithGroup(name)}
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
