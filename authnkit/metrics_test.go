package authnkit_test

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

func TestMetricsEmitted(t *testing.T) {
	ctx := t.Context()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	jwt, err := authnkit.NewHS256JWT(authnkit.JWTConfig{Secret: "test-secret-key-of-at-least-32-bytes!!", AccessTTL: time.Minute})
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	tok, err := jwt.SignAccess(ctx, map[string]any{"sub": "u1"})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := jwt.ParseAccess(ctx, tok); err != nil {
		t.Fatalf("parse ok: %v", err)
	}
	if _, err := jwt.Parse(ctx, "not.a.token"); err == nil {
		t.Fatal("expected error on malformed token")
	}
	if _, err := jwt.ParseRefresh(ctx, tok); err == nil {
		t.Fatal("expected error on wrong token type")
	}

	hasher, err := authnkit.NewArgon2idHasher(authnkit.Argon2idParams{
		Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8,
	})
	if err != nil {
		t.Fatalf("NewArgon2idHasher: %v", err)
	}
	hash, err := hasher.Hash(ctx, "password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := hasher.Verify(ctx, "password", hash); err != nil {
		t.Fatalf("verify: %v", err)
	}
	hasher.VerifyDummy(ctx)

	totp, err := authnkit.NewTOTP(authnkit.TOTPConfig{Issuer: "t", Skew: 1})
	if err != nil {
		t.Fatalf("NewTOTP: %v", err)
	}
	if ok, _ := totp.Verify(ctx, "JBSWY3DPEHPK3PXP", "000000"); ok {
		t.Log("statistically improbable TOTP match")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"authn_jwt_verifications_total":          false,
		"authn_password_hash_duration_seconds":   false,
		"authn_password_verify_duration_seconds": false,
		"authn_totp_verifications_total":         false,
	}
	buckets := map[string]bool{
		"authn_password_hash_duration_seconds":   false,
		"authn_password_verify_duration_seconds": false,
	}
	results := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if _, ok := want[m.Name]; ok {
				want[m.Name] = true
			}
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok {
				if _, tracked := buckets[m.Name]; tracked && len(h.DataPoints) > 0 {
					// Дефолтные бакеты SDK рассчитаны на миллисекунды: границы
					// кита начинаются с 5 мс.
					buckets[m.Name] = h.DataPoints[0].Bounds[0] == 0.005
				}
			}
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == "authn_jwt_verifications_total" {
				for _, dp := range sum.DataPoints {
					if v, found := dp.Attributes.Value("result"); found {
						results[v.AsString()] = true
					}
				}
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %s not emitted", name)
		}
	}
	for name, ok := range buckets {
		if !ok {
			t.Errorf("metric %s has no explicit duration buckets", name)
		}
	}
	for _, result := range []string{"ok", "malformed", "wrong_type"} {
		if !results[result] {
			t.Errorf("authn_jwt_verifications_total has no result=%q series", result)
		}
	}
}
