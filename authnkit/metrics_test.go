package authnkit_test

import (
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func TestMetricsEmitted(t *testing.T) {
	ctx := t.Context()
	before := metrictest.Collect(t)

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
	untyped, err := jwt.Sign(ctx, map[string]any{"sub": "u1"}, time.Minute)
	if err != nil {
		t.Fatalf("sign untyped: %v", err)
	}
	if _, err := jwt.ParseAccess(ctx, untyped); err == nil {
		t.Fatal("expected error on wrong token type")
	}

	hasher, err := authnkit.NewArgon2idHasher(authnkit.Argon2idConfig{
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

	d := metrictest.Collect(t).Sub(before)
	for _, name := range []string{"authn_password_hash_duration_seconds", "authn_password_verify_duration_seconds"} {
		// Дефолтные бакеты SDK рассчитаны на миллисекунды: границы кита начинаются с 5 мс.
		if s := d.Get(t, name); s.Kind != metrictest.Histogram || s.Unit != "s" || len(s.Bounds) == 0 || s.Bounds[0] != 0.005 {
			t.Errorf("%v (%s, unit %q, bounds %v), want histogram in s on the shared buckets", s, s.Kind, s.Unit, s.Bounds)
		}
	}
	for _, result := range []string{"ok", "malformed", "wrong_type"} {
		if s := d.Get(t, "authn_jwt_verifications_total", "result", result); s.Kind != metrictest.Counter || s.Value != 1 {
			t.Errorf("%v (%s), want counter delta 1", s, s.Kind)
		}
	}
	if got := d.Find("authn_totp_verifications_total"); len(got) == 0 {
		t.Error("metric authn_totp_verifications_total not emitted")
	}
}
