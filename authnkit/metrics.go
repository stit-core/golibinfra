package authnkit

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instruments — singleton (sync.Once) чтобы повторные NewHS256JWT/
// NewArgon2idHasher не пытались зарегистрировать одни и те же имена и
// не словили "instrument already exists" в OTel SDK.
var (
	instrOnce sync.Once
	instr     *authnInstruments
)

type authnInstruments struct {
	jwtVerifications  metric.Int64Counter
	hashDuration      metric.Float64Histogram
	verifyDuration    metric.Float64Histogram
	totpVerifications metric.Int64Counter
}

func getInstruments() *authnInstruments {
	instrOnce.Do(func() {
		m := otel.Meter("git.stit.tech/stit-infra/golibinfra/authnkit")
		jwt, _ := m.Int64Counter("jwt_verifications_total",
			metric.WithDescription("JWT verification outcomes"),
		)
		hash, _ := m.Float64Histogram("password_hash_duration_seconds",
			metric.WithDescription("Time to hash a password (argon2id)"),
			metric.WithUnit("s"),
		)
		verify, _ := m.Float64Histogram("password_verify_duration_seconds",
			metric.WithDescription("Time to verify a password against an argon2id hash"),
			metric.WithUnit("s"),
		)
		totp, _ := m.Int64Counter("totp_verifications_total",
			metric.WithDescription("TOTP verification outcomes"),
		)
		instr = &authnInstruments{
			jwtVerifications:  jwt,
			hashDuration:      hash,
			verifyDuration:    verify,
			totpVerifications: totp,
		}
	})
	return instr
}

func recordJWTResult(result string) {
	getInstruments().jwtVerifications.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("result", result)),
	)
}

func recordTOTPResult(result string) {
	getInstruments().totpVerifications.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("result", result)),
	)
}
