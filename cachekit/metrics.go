package cachekit

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type observeHook struct {
	commands metric.Int64Counter
	duration metric.Float64Histogram
	tracer   trace.Tracer
}

func newObserveHook() *observeHook {
	m := otelkit.Meter("cachekit")
	return &observeHook{
		commands: otelkit.Must(m.Int64Counter("cache_commands_total",
			metric.WithDescription("Redis commands by name and result (ok|miss|error)"))),
		duration: otelkit.Must(m.Float64Histogram("cache_command_duration_seconds",
			metric.WithDescription("Redis command round-trip duration"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets())),
		tracer: otelkit.Tracer("cachekit"),
	}
}

func (h *observeHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h *observeHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		ctx, span := h.start(ctx, cmd.Name())
		start := time.Now()
		err := next(ctx, cmd)
		h.record(ctx, cmd.Name(), start, err)
		h.end(span, err)
		return err
	}
}

func (h *observeHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		ctx, span := h.start(ctx, "pipeline")
		span.SetAttributes(attribute.Int("db.redis.batch_size", len(cmds)))
		start := time.Now()
		err := next(ctx, cmds)
		h.record(ctx, "pipeline", start, err)
		h.end(span, err)
		return err
	}
}

// start — ключи и значения в атрибуты не кладём: PII и неограниченная кардинальность.
func (h *observeHook) start(ctx context.Context, cmd string) (context.Context, trace.Span) {
	return h.tracer.Start(ctx, "redis "+cmd,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", cmd),
		))
}

func (h *observeHook) end(span trace.Span, err error) {
	if err != nil && !errors.Is(err, redis.Nil) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func (h *observeHook) record(ctx context.Context, cmd string, start time.Time, err error) {
	result := "ok"
	switch {
	case errors.Is(err, redis.Nil):
		result = "miss"
	case err != nil:
		result = "error"
	}
	h.commands.Add(ctx, 1, metric.WithAttributes(
		attribute.String("cmd", cmd),
		attribute.String("result", result),
	))
	h.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("cmd", cmd)))
}
