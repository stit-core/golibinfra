package amocrmkit

import (
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type instruments struct {
	apiCalls     metric.Int64Counter     // amocrm_api_calls_total{endpoint,status}
	apiDuration  metric.Float64Histogram // amocrm_api_duration_seconds{endpoint}
	rateLimited  metric.Int64Counter     // amocrm_rate_limited_total{account}
	oauthRefresh metric.Int64Counter     // amocrm_oauth_refresh_total{status}
}

func newInstruments() *instruments {
	m := otelkit.Meter("amocrmkit")
	return &instruments{
		apiCalls: otelkit.Must(m.Int64Counter("amocrm_api_calls_total",
			metric.WithDescription("Outbound amoCRM API calls by endpoint and HTTP status"))),
		apiDuration: otelkit.Must(m.Float64Histogram("amocrm_api_duration_seconds",
			metric.WithDescription("Outbound amoCRM API call duration"),
			metric.WithUnit("s"))),
		rateLimited: otelkit.Must(m.Int64Counter("amocrm_rate_limited_total",
			metric.WithDescription("amoCRM responses throttled (429) per account"))),
		oauthRefresh: otelkit.Must(m.Int64Counter("amocrm_oauth_refresh_total",
			metric.WithDescription("OAuth token exchange/refresh outcomes by status"))),
	}
}
