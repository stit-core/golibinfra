package googleaikit

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type instruments struct {
	calls   metric.Int64Counter     // google_api_calls_total{model,op,status}
	latency metric.Float64Histogram // google_api_latency_seconds{model,op}
	tokens  metric.Int64Counter     // google_api_tokens_total{model,kind}
	cost    metric.Int64Counter     // google_api_cost_micros_total{model}
}

func newInstruments() *instruments {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/googleaikit")

	calls, err := m.Int64Counter(
		"google_api_calls_total",
		metric.WithDescription("Gemini API calls by model, op and status"),
	)
	if err != nil {
		panic(fmt.Sprintf("googleaikit: counter calls: %v", err))
	}
	lat, err := m.Float64Histogram(
		"google_api_latency_seconds",
		metric.WithDescription("Gemini API call latency"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(fmt.Sprintf("googleaikit: histogram latency: %v", err))
	}
	toks, err := m.Int64Counter(
		"google_api_tokens_total",
		metric.WithDescription("Gemini tokens by model and kind"),
	)
	if err != nil {
		panic(fmt.Sprintf("googleaikit: counter tokens: %v", err))
	}
	cost, err := m.Int64Counter(
		"google_api_cost_micros_total",
		metric.WithDescription("Gemini spend in micro-USD by model"),
	)
	if err != nil {
		panic(fmt.Sprintf("googleaikit: counter cost: %v", err))
	}

	return &instruments{calls: calls, latency: lat, tokens: toks, cost: cost}
}

// record фиксирует исход одного вызова. op различает модальности
// (generate/embed/image/video/audio) на общих метриках.
func (i *instruments) record(ctx context.Context, model, op, status string, dur time.Duration, u Usage, cost USDMicros) {
	i.calls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("model", model),
		attribute.String("op", op),
		attribute.String("status", status),
	))
	i.latency.Record(ctx, dur.Seconds(), metric.WithAttributes(
		attribute.String("model", model),
		attribute.String("op", op),
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
	tok("cached", u.CachedTokens)
	tok("reasoning", u.ReasoningTokens)
	tok("tool_use", u.ToolUseTokens)
	if cost > 0 {
		i.cost.Add(ctx, int64(cost), metric.WithAttributes(attribute.String("model", model)))
	}
}
