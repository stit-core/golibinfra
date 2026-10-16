package authnkit

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

type hashInstruments struct {
	hashDuration   metric.Float64Histogram
	verifyDuration metric.Float64Histogram
	busy           metric.Int64Counter
}

func newHashInstruments() *hashInstruments {
	m := otelkit.Meter("authnkit")
	return &hashInstruments{
		hashDuration: otelkit.Must(m.Float64Histogram("authn_password_hash_duration_seconds",
			metric.WithDescription("Time to hash a password (argon2id)"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
		verifyDuration: otelkit.Must(m.Float64Histogram("authn_password_verify_duration_seconds",
			metric.WithDescription("Time to verify a password against an argon2id hash"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
		busy: otelkit.Must(m.Int64Counter("authn_password_busy_total",
			metric.WithDescription("Password hashing and verification refused with ErrBusy: the argon2id queue is full"),
		)),
	}
}

type jwtInstruments struct {
	verifications metric.Int64Counter
}

func newJWTInstruments() *jwtInstruments {
	m := otelkit.Meter("authnkit")
	return &jwtInstruments{
		verifications: otelkit.Must(m.Int64Counter("authn_jwt_verifications_total",
			metric.WithDescription("JWT verification outcomes"),
		)),
	}
}

func (i *jwtInstruments) recordJWT(ctx context.Context, result string) {
	i.verifications.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

type totpInstruments struct {
	verifications metric.Int64Counter
}

func newTOTPInstruments() *totpInstruments {
	m := otelkit.Meter("authnkit")
	return &totpInstruments{
		verifications: otelkit.Must(m.Int64Counter("authn_totp_verifications_total",
			metric.WithDescription("TOTP verification outcomes"),
		)),
	}
}

func (i *totpInstruments) recordTOTP(ctx context.Context, result string) {
	i.verifications.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
