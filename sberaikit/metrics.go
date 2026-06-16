package sberaikit

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type instruments struct {
	calls   metric.Int64Counter     // sber_api_calls_total{op,model,status}
	latency metric.Float64Histogram // sber_api_latency_seconds{op,model}
	tokens  metric.Int64Counter     // sber_api_tokens_total{model,kind}
	cost    metric.Int64Counter     // sber_api_cost_micros_total{model}
}

func newInstruments() *instruments {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/sberaikit")

	calls, err := m.Int64Counter(
		"sber_api_calls_total",
		metric.WithDescription("GigaChat API calls by op, model and status"),
	)
	if err != nil {
		panic(fmt.Sprintf("sberaikit: counter calls: %v", err))
	}
	lat, err := m.Float64Histogram(
		"sber_api_latency_seconds",
		metric.WithDescription("GigaChat API call latency"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(fmt.Sprintf("sberaikit: histogram latency: %v", err))
	}
	toks, err := m.Int64Counter(
		"sber_api_tokens_total",
		metric.WithDescription("GigaChat tokens by model and kind"),
	)
	if err != nil {
		panic(fmt.Sprintf("sberaikit: counter tokens: %v", err))
	}
	cost, err := m.Int64Counter(
		"sber_api_cost_micros_total",
		metric.WithDescription("GigaChat spend in micro-RUB by model"),
	)
	if err != nil {
		panic(fmt.Sprintf("sberaikit: counter cost: %v", err))
	}

	return &instruments{calls: calls, latency: lat, tokens: toks, cost: cost}
}

func (i *instruments) record(ctx context.Context, op, model, status string, dur time.Duration, u Usage, cost RUBMicros) {
	i.calls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("op", op),
		attribute.String("model", model),
		attribute.String("status", status),
	))
	i.latency.Record(ctx, dur.Seconds(), metric.WithAttributes(
		attribute.String("op", op),
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
	tok("cached", u.CachedTokens)
	if cost > 0 {
		i.cost.Add(ctx, int64(cost), metric.WithAttributes(attribute.String("model", model)))
	}
}
