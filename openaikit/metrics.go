package openaikit

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type instruments struct {
	calls   metric.Int64Counter     // openai_api_calls_total{model,modality,status}
	latency metric.Float64Histogram // openai_api_latency_seconds{model,modality}
	tokens  metric.Int64Counter     // openai_api_tokens_total{model,kind}
	cost    metric.Int64Counter     // openai_api_cost_micros_total{model}
}

func newInstruments() *instruments {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/openaikit")

	calls, err := m.Int64Counter(
		"openai_api_calls_total",
		metric.WithDescription("OpenAI API calls by model, modality and status"),
	)
	if err != nil {
		panic(fmt.Sprintf("openaikit: counter calls: %v", err))
	}
	lat, err := m.Float64Histogram(
		"openai_api_latency_seconds",
		metric.WithDescription("OpenAI API call latency"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(fmt.Sprintf("openaikit: histogram latency: %v", err))
	}
	toks, err := m.Int64Counter(
		"openai_api_tokens_total",
		metric.WithDescription("OpenAI tokens by model and kind"),
	)
	if err != nil {
		panic(fmt.Sprintf("openaikit: counter tokens: %v", err))
	}
	cost, err := m.Int64Counter(
		"openai_api_cost_micros_total",
		metric.WithDescription("OpenAI spend in micro-USD by model"),
	)
	if err != nil {
		panic(fmt.Sprintf("openaikit: counter cost: %v", err))
	}

	return &instruments{calls: calls, latency: lat, tokens: toks, cost: cost}
}

func (i *instruments) record(ctx context.Context, model string, mod Modality, status string, dur time.Duration, u Usage, cost USDMicros) {
	i.calls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("model", model),
		attribute.String("modality", string(mod)),
		attribute.String("status", status),
	))
	i.latency.Record(ctx, dur.Seconds(), metric.WithAttributes(
		attribute.String("model", model),
		attribute.String("modality", string(mod)),
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
	if cost > 0 {
		i.cost.Add(ctx, int64(cost), metric.WithAttributes(attribute.String("model", model)))
	}
}
