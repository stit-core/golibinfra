package cachekit

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type metricsHook struct {
	commands metric.Int64Counter
	duration metric.Float64Histogram
}

func newMetricsHook() *metricsHook {
	return newMetricsHookWithMeter(otelkit.Meter("cachekit"))
}

func newMetricsHookWithMeter(m metric.Meter) *metricsHook {
	return &metricsHook{
		commands: otelkit.Must(m.Int64Counter("cache_commands_total",
			metric.WithDescription("Redis commands by name and result (ok|miss|error)"))),
		duration: otelkit.Must(m.Float64Histogram("cache_command_duration_seconds",
			metric.WithDescription("Redis command round-trip duration"),
			metric.WithUnit("s"))),
	}
}

func (h *metricsHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h *metricsHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmd)
		h.record(ctx, cmd.Name(), start, err)
		return err
	}
}

func (h *metricsHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmds)
		h.record(ctx, "pipeline", start, err)
		return err
	}
}

func (h *metricsHook) record(ctx context.Context, cmd string, start time.Time, err error) {
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
