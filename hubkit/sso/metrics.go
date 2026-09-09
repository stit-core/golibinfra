package sso

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type instruments struct {
	contour       attribute.KeyValue
	verifications metric.Int64Counter
	jwksRefresh   metric.Int64Counter
}

func newInstruments(contour string) *instruments {
	m := otelkit.Meter("hubkit/sso")
	return &instruments{
		contour: attribute.String("contour", contour),
		verifications: otelkit.Must(m.Int64Counter(
			"hub_sso_verifications_total",
			metric.WithDescription("Hub-session token verification outcomes"),
		)),
		jwksRefresh: otelkit.Must(m.Int64Counter(
			"hub_sso_jwks_refresh_total",
			metric.WithDescription("JWKS fetch outcomes"),
		)),
	}
}

func (i *instruments) verified(ctx context.Context, result string) {
	i.verifications.Add(ctx, 1, metric.WithAttributes(i.contour, attribute.String("result", result)))
}

func (i *instruments) refreshed(ctx context.Context, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	i.jwksRefresh.Add(ctx, 1, metric.WithAttributes(i.contour, attribute.String("result", result)))
}
