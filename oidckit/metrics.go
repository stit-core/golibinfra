package oidckit

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type instruments struct {
	verifications metric.Int64Counter
	jwksRefresh   metric.Int64Counter
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
	}
}

func (i *instruments) verified(ctx context.Context, result string) {
	i.verifications.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

func (i *instruments) refreshed(ctx context.Context, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	i.jwksRefresh.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
