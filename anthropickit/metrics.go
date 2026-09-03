package anthropickit

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

const (
	opComplete    = "complete"
	opStream      = "stream"
	opCountTokens = "count_tokens"

	statusOK        = "ok"
	statusError     = "error"
	statusAbandoned = "abandoned"
)

type instruments struct {
	calls   metric.Int64Counter     // anthropic_api_calls_total{model,op,status,priced}
	latency metric.Float64Histogram // anthropic_api_latency_seconds{model,op}
	tokens  metric.Int64Counter     // anthropic_api_tokens_total{model,kind}
	cost    metric.Int64Counter     // anthropic_api_cost_micros_total{model}
}

func newInstruments() *instruments {
	m := otelkit.Meter("anthropickit")
	return &instruments{
		calls: otelkit.Must(m.Int64Counter("anthropic_api_calls_total",
			metric.WithDescription("Claude API calls by model, operation, status and pricing coverage"))),
		latency: otelkit.Must(m.Float64Histogram("anthropic_api_latency_seconds",
			metric.WithDescription("Claude API call latency"),
			metric.WithUnit("s"))),
		tokens: otelkit.Must(m.Int64Counter("anthropic_api_tokens_total",
			metric.WithDescription("Claude tokens by model and kind"))),
		cost: otelkit.Must(m.Int64Counter("anthropic_api_cost_micros_total",
			metric.WithDescription("Claude spend in micro-USD by model"))),
	}
}

// callResult — итог одного вызова API для метрик.
type callResult struct {
	op     string
	model  string
	status string
	priced bool
	dur    time.Duration
	usage  Usage
	cost   USDMicros
}

func (i *instruments) record(ctx context.Context, r callResult) {
	i.calls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("model", r.model),
		attribute.String("op", r.op),
		attribute.String("status", r.status),
		attribute.Bool("priced", r.priced),
	))
	i.latency.Record(ctx, r.dur.Seconds(), metric.WithAttributes(
		attribute.String("model", r.model),
		attribute.String("op", r.op),
	))
	tok := func(kind string, n int64) {
		if n == 0 {
			return
		}
		i.tokens.Add(ctx, n, metric.WithAttributes(
			attribute.String("model", r.model),
			attribute.String("kind", kind),
		))
	}
	tok("input", r.usage.InputTokens)
	tok("output", r.usage.OutputTokens)
	tok("cache_write", r.usage.CacheWrite5mTokens+r.usage.CacheWrite1hTokens)
	tok("cache_read", r.usage.CacheReadTokens)
	tok("thinking", r.usage.ThinkingTokens)
	if r.cost > 0 {
		i.cost.Add(ctx, int64(r.cost), metric.WithAttributes(attribute.String("model", r.model)))
	}
}
