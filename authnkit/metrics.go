package authnkit

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type instruments struct {
	jwtVerifications  metric.Int64Counter
	hashDuration      metric.Float64Histogram
	verifyDuration    metric.Float64Histogram
	totpVerifications metric.Int64Counter
}

func newInstruments() *instruments {
	m := otelkit.Meter("authnkit")
	return &instruments{
		jwtVerifications: otelkit.Must(m.Int64Counter("jwt_verifications_total",
			metric.WithDescription("JWT verification outcomes"),
		)),
		hashDuration: otelkit.Must(m.Float64Histogram("password_hash_duration_seconds",
			metric.WithDescription("Time to hash a password (argon2id)"),
			metric.WithUnit("s"),
		)),
		verifyDuration: otelkit.Must(m.Float64Histogram("password_verify_duration_seconds",
			metric.WithDescription("Time to verify a password against an argon2id hash"),
			metric.WithUnit("s"),
		)),
		totpVerifications: otelkit.Must(m.Int64Counter("totp_verifications_total",
			metric.WithDescription("TOTP verification outcomes"),
		)),
	}
}

func (i *instruments) recordJWT(result string) {
	i.jwtVerifications.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("result", result)),
	)
}

func (i *instruments) recordTOTP(result string) {
	i.totpVerifications.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("result", result)),
	)
}
