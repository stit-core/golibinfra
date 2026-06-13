package amocrmkit

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

type instruments struct {
	apiCalls     metric.Int64Counter     // amocrm_api_calls_total{endpoint,status}
	apiDuration  metric.Float64Histogram // amocrm_api_duration_seconds{endpoint}
	rateLimited  metric.Int64Counter     // amocrm_rate_limited_total{account}
	oauthRefresh metric.Int64Counter     // amocrm_oauth_refresh_total{status}
}

func newInstruments() *instruments {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/amocrmkit")

	calls, err := m.Int64Counter(
		"amocrm_api_calls_total",
		metric.WithDescription("Outbound amoCRM API calls by endpoint and HTTP status"),
	)
	if err != nil {
		panic(fmt.Sprintf("amocrmkit: counter api_calls: %v", err))
	}
	dur, err := m.Float64Histogram(
		"amocrm_api_duration_seconds",
		metric.WithDescription("Outbound amoCRM API call duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		panic(fmt.Sprintf("amocrmkit: histogram api_duration: %v", err))
	}
	rl, err := m.Int64Counter(
		"amocrm_rate_limited_total",
		metric.WithDescription("amoCRM responses throttled (429) per account"),
	)
	if err != nil {
		panic(fmt.Sprintf("amocrmkit: counter rate_limited: %v", err))
	}
	refresh, err := m.Int64Counter(
		"amocrm_oauth_refresh_total",
		metric.WithDescription("OAuth token exchange/refresh outcomes by status"),
	)
	if err != nil {
		panic(fmt.Sprintf("amocrmkit: counter oauth_refresh: %v", err))
	}

	return &instruments{
		apiCalls:     calls,
		apiDuration:  dur,
		rateLimited:  rl,
		oauthRefresh: refresh,
	}
}
