package authnkit_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

func TestMetricsEmitted(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	jwt, err := authnkit.NewHS256JWT(authnkit.JWTConfig{Secret: "test-secret-key-of-at-least-32-bytes!!", AccessTTL: time.Minute})
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	tok, err := jwt.Sign(map[string]any{"sub": "u1"}, time.Minute)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := jwt.Parse(tok); err != nil {
		t.Fatalf("parse ok: %v", err)
	}
	if _, err := jwt.Parse("not.a.token"); err == nil {
		t.Fatal("expected error on malformed token")
	}

	hasher := authnkit.NewArgon2idHasher(authnkit.Argon2idParams{
		Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8,
	})
	hash, err := hasher.Hash("password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := hasher.Verify("password", hash); err != nil {
		t.Fatalf("verify: %v", err)
	}
	totp := authnkit.NewTOTP(authnkit.TOTPConfig{Issuer: "t", Skew: 1})
	if totp.Verify("JBSWY3DPEHPK3PXP", "000000") {
		t.Log("statistically improbable TOTP match")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"jwt_verifications_total":          false,
		"password_hash_duration_seconds":   false,
		"password_verify_duration_seconds": false,
		"totp_verifications_total":         false,
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if _, ok := want[m.Name]; ok {
				want[m.Name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %s not emitted", name)
		}
	}
}
