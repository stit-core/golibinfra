package claudekit

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type instruments struct {
	calls   metric.Int64Counter     // claude_api_calls_total{model,status}
	latency metric.Float64Histogram // claude_api_latency_seconds{model}
	tokens  metric.Int64Counter     // claude_api_tokens_total{model,kind}
	cost    metric.Int64Counter     // claude_api_cost_micros_total{model}
}

func newInstruments() *instruments {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/claudekit")

	calls, err := m.Int64Counter(
		"claude_api_calls_total",
		metric.WithDescription("Claude API calls by model and status"),
	)
	if err != nil {
		panic(fmt.Sprintf("claudekit: counter calls: %v", err))
	}
	lat, err := m.Float64Histogram(
		"claude_api_latency_seconds",
		metric.WithDescription("Claude API call latency"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(fmt.Sprintf("claudekit: histogram latency: %v", err))
	}
	toks, err := m.Int64Counter(
		"claude_api_tokens_total",
		metric.WithDescription("Claude tokens by model and kind"),
	)
	if err != nil {
		panic(fmt.Sprintf("claudekit: counter tokens: %v", err))
	}
	cost, err := m.Int64Counter(
		"claude_api_cost_micros_total",
		metric.WithDescription("Claude spend in micro-USD by model"),
	)
	if err != nil {
		panic(fmt.Sprintf("claudekit: counter cost: %v", err))
	}

	return &instruments{calls: calls, latency: lat, tokens: toks, cost: cost}
}

func (i *instruments) record(ctx context.Context, model, status string, dur time.Duration, u Usage, cost USDMicros) {
	i.calls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("model", model),
		attribute.String("status", status),
	))
	i.latency.Record(ctx, dur.Seconds(), metric.WithAttributes(
		attribute.String("model", model),
	))
	if status != "ok" {
		return
	}
	tok := func(kind string, n int64) {
		if n == 0 {
			return
		}
		i.tokens.Add(ctx, n, metric.WithAttributes(
			attribute.String("model", model),
			attribute.String("kind", kind),
		))
	}
	tok("input", u.InputTokens)
	tok("output", u.OutputTokens)
	tok("cache_write", u.CacheWrite5mTokens+u.CacheWrite1hTokens)
	tok("cache_read", u.CacheReadTokens)
	tok("thinking", u.ThinkingTokens)
	if cost > 0 {
		i.cost.Add(ctx, int64(cost), metric.WithAttributes(attribute.String("model", model)))
	}
}
