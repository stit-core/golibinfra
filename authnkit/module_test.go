package authnkit_test

import (
	"errors"
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

func TestModule_Graph(t *testing.T) {
	var (
		hasher   authnkit.Hasher
		argon    *authnkit.Argon2idHasher
		signer   authnkit.JWTSigner
		parser   authnkit.JWTParser
		jwt      *authnkit.HS256JWT
		totp     *authnkit.TOTPGenerator
		registry *authnkit.Registry
	)
	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			authnkit.Argon2idParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8},
			authnkit.JWTConfig{Secret: "test-secret-key-of-at-least-32-bytes!!"},
			authnkit.TOTPConfig{Issuer: "svc", Skew: 1},
		),
		authnkit.Module,
		fx.Populate(&hasher, &argon, &signer, &parser, &jwt, &totp, &registry),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("graph: %v", err)
	}
	if hasher == nil || argon == nil || signer == nil || parser == nil || jwt == nil || totp == nil || registry == nil {
		t.Fatal("dependencies not populated")
	}
	// Rehash-flow из доков достижим через граф.
	if _, err := hasher.Hash(t.Context(), "pw"); err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !hasher.NeedsRehash("garbage") {
		t.Fatal("Hasher from the graph must expose NeedsRehash")
	}
}

func TestModule_InvalidConfigFailsGraph(t *testing.T) {
	cases := map[string]fx.Option{
		"short jwt secret": fx.Supply(
			authnkit.Argon2idParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8},
			authnkit.JWTConfig{Secret: "short"},
		),
		"zero argon params": fx.Supply(
			authnkit.Argon2idParams{},
			authnkit.JWTConfig{Secret: "test-secret-key-of-at-least-32-bytes!!"},
		),
		"argon time above window": fx.Supply(
			authnkit.Argon2idParams{Time: authnkit.MaxTime + 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8},
			authnkit.JWTConfig{Secret: "test-secret-key-of-at-least-32-bytes!!"},
		),
	}
	for name, supply := range cases {
		t.Run(name, func(t *testing.T) {
			app := fx.New(
				fx.NopLogger,
				supply,
				authnkit.Module,
				fx.Invoke(func(authnkit.Hasher, *authnkit.HS256JWT) {}),
			)
			err := app.Err()
			if !errors.Is(err, authnkit.ErrConfig) {
				t.Fatalf("expected ErrConfig from the graph, got %v", err)
			}
		})
	}

	// TOTP лениво: невалидный TOTPConfig валит граф только у того, кто просит генератор.
	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			authnkit.Argon2idParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8},
			authnkit.JWTConfig{Secret: "test-secret-key-of-at-least-32-bytes!!"},
			authnkit.TOTPConfig{},
		),
		authnkit.Module,
		fx.Invoke(func(*authnkit.TOTPGenerator) {}),
	)
	if err := app.Err(); !errors.Is(err, authnkit.ErrConfig) {
		t.Fatalf("expected ErrConfig from the graph, got %v", err)
	}
}

func TestNewConfigsFromEnv(t *testing.T) {
	t.Run("argon2id", func(t *testing.T) {
		t.Setenv("ARGON2_TIME", "2")
		t.Setenv("ARGON2_MEMORY_KIB", "16384")
		p, err := authnkit.NewArgon2idParams()
		if err != nil {
			t.Fatalf("NewArgon2idParams: %v", err)
		}
		if p.Time != 2 || p.Memory != 16384 || p.Threads != 4 || p.KeyLen != 32 || p.SaltLen != 16 {
			t.Fatalf("params = %+v", p)
		}

		t.Setenv("ARGON2_TIME", "0")
		if _, err := authnkit.NewArgon2idParams(); !errors.Is(err, authnkit.ErrConfig) {
			t.Fatalf("ARGON2_TIME=0: expected ErrConfig, got %v", err)
		}
		t.Setenv("ARGON2_TIME", "not-a-number")
		if _, err := authnkit.NewArgon2idParams(); !errors.Is(err, authnkit.ErrConfig) {
			t.Fatalf("ARGON2_TIME=not-a-number: expected ErrConfig, got %v", err)
		}
	})

	t.Run("jwt", func(t *testing.T) {
		t.Setenv("JWT_SECRET", "env-secret-key-of-at-least-32-bytes!!!")
		t.Setenv("JWT_ACCESS_TTL", "5m")
		c, err := authnkit.NewJWTConfig()
		if err != nil {
			t.Fatalf("NewJWTConfig: %v", err)
		}
		if c.AccessTTL.Minutes() != 5 || c.RefreshTTL != authnkit.DefaultRefreshTTL || c.Leeway != authnkit.DefaultLeeway {
			t.Fatalf("config = %+v", c)
		}

		t.Setenv("JWT_SECRET", "short")
		if _, err := authnkit.NewJWTConfig(); !errors.Is(err, authnkit.ErrConfig) {
			t.Fatalf("short JWT_SECRET: expected ErrConfig, got %v", err)
		}
		t.Setenv("JWT_SECRET", "env-secret-key-of-at-least-32-bytes!!!")
		t.Setenv("JWT_ACCESS_TTL", "not-a-duration")
		if _, err := authnkit.NewJWTConfig(); !errors.Is(err, authnkit.ErrConfig) {
			t.Fatalf("bad JWT_ACCESS_TTL: expected ErrConfig, got %v", err)
		}
	})

	t.Run("totp", func(t *testing.T) {
		t.Setenv("TOTP_ISSUER", "My Service")
		c, err := authnkit.NewTOTPConfig()
		if err != nil {
			t.Fatalf("NewTOTPConfig: %v", err)
		}
		if c.Issuer != "My Service" || c.Skew != 1 {
			t.Fatalf("config = %+v", c)
		}

		t.Setenv("TOTP_SKEW", "10")
		if _, err := authnkit.NewTOTPConfig(); !errors.Is(err, authnkit.ErrConfig) {
			t.Fatalf("TOTP_SKEW=10: expected ErrConfig, got %v", err)
		}
		t.Setenv("TOTP_SKEW", "not-a-number")
		if _, err := authnkit.NewTOTPConfig(); !errors.Is(err, authnkit.ErrConfig) {
			t.Fatalf("bad TOTP_SKEW: expected ErrConfig, got %v", err)
		}
		t.Setenv("TOTP_SKEW", "1")
		t.Setenv("TOTP_ISSUER", "My:Service")
		if _, err := authnkit.NewTOTPConfig(); !errors.Is(err, authnkit.ErrConfig) {
			t.Fatalf("TOTP_ISSUER with ':': expected ErrConfig, got %v", err)
		}
	})
}
