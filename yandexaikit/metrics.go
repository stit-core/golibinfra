package yandexaikit

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type instruments struct {
	calls   metric.Int64Counter     // yandex_api_calls_total{endpoint,model,status}
	latency metric.Float64Histogram // yandex_api_latency_seconds{endpoint,model}
	tokens  metric.Int64Counter     // yandex_api_tokens_total{model,kind}
	cost    metric.Int64Counter     // yandex_api_cost_micros_total{model}
}

func newInstruments() *instruments {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/yandexaikit")

	calls, err := m.Int64Counter(
		"yandex_api_calls_total",
		metric.WithDescription("Yandex FM API calls by endpoint, model and status"),
	)
	if err != nil {
		panic(fmt.Sprintf("yandexaikit: counter calls: %v", err))
	}
	lat, err := m.Float64Histogram(
		"yandex_api_latency_seconds",
		metric.WithDescription("Yandex FM API call latency"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(fmt.Sprintf("yandexaikit: histogram latency: %v", err))
	}
	toks, err := m.Int64Counter(
		"yandex_api_tokens_total",
		metric.WithDescription("Yandex FM tokens by model and kind"),
	)
	if err != nil {
		panic(fmt.Sprintf("yandexaikit: counter tokens: %v", err))
	}
	cost, err := m.Int64Counter(
		"yandex_api_cost_micros_total",
		metric.WithDescription("Yandex FM spend in micro-RUB by model"),
	)
	if err != nil {
		panic(fmt.Sprintf("yandexaikit: counter cost: %v", err))
	}

	return &instruments{calls: calls, latency: lat, tokens: toks, cost: cost}
}

// recordUsage пишет токены (по kind) и cost успешного вызова. calls/latency
// фиксируются отдельно в Client.recordCall (на каждом вызове, включая ошибки).
func (i *instruments) recordUsage(ctx context.Context, model string, u Usage, cost RUBMicros) {
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
	tok("reasoning", u.ReasoningTokens)
	if cost > 0 {
		i.cost.Add(ctx, int64(cost), metric.WithAttributes(attribute.String("model", model)))
	}
}
