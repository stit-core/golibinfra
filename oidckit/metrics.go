package oidckit

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type instruments struct {
	verifications metric.Int64Counter
	jwksRefresh   metric.Int64Counter
	logins        metric.Int64Counter
	discovery     metric.Float64Histogram
}

func newInstruments() *instruments {
	m := otelkit.Meter("oidckit")
	return &instruments{
		verifications: otelkit.Must(m.Int64Counter("oidc_verifications_total",
			metric.WithDescription("ID-token verification outcomes"),
		)),
		jwksRefresh: otelkit.Must(m.Int64Counter("oidc_jwks_refresh_total",
			metric.WithDescription("JWKS fetch outcomes"),
		)),
		logins: otelkit.Must(m.Int64Counter("oidc_logins_total",
			metric.WithDescription("BFF login flow outcomes by stage"),
		)),
		discovery: otelkit.Must(m.Float64Histogram("oidc_discovery_duration_seconds",
			metric.WithDescription("OIDC discovery and initial JWKS load"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
	}
}

func (i *instruments) verified(ctx context.Context, result string) {
	i.verifications.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

func (i *instruments) refreshed(ctx context.Context, err error) {
	i.jwksRefresh.Add(ctx, 1, metric.WithAttributes(attribute.String("result", resultOf(err))))
}

func (i *instruments) login(ctx context.Context, stage, result string) {
	i.logins.Add(ctx, 1, metric.WithAttributes(
		attribute.String("stage", stage),
		attribute.String("result", result),
	))
}

func (i *instruments) discovered(ctx context.Context, d time.Duration, err error) {
	i.discovery.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("result", resultOf(err))))
}

func resultOf(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
