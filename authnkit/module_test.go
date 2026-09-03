package authnkit_test

import (
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

func TestModule_Graph(t *testing.T) {
	var (
		hasher   authnkit.Hasher
		signer   authnkit.JWTSigner
		parser   authnkit.JWTParser
		jwt      *authnkit.HS256JWT
		registry *authnkit.Registry
	)
	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			authnkit.Argon2idParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8},
			authnkit.JWTConfig{Secret: "test-secret-key-of-at-least-32-bytes!!"},
		),
		authnkit.Module,
		fx.Populate(&hasher, &signer, &parser, &jwt, &registry),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("graph: %v", err)
	}
	if hasher == nil || signer == nil || parser == nil || jwt == nil || registry == nil {
		t.Fatal("dependencies not populated")
	}

	// Короткий секрет валит сборку графа, а не первый Sign.
	bad := fx.New(
		fx.NopLogger,
		fx.Supply(authnkit.Argon2idParams{}, authnkit.JWTConfig{Secret: "short"}),
		authnkit.Module,
		fx.Invoke(func(*authnkit.HS256JWT) {}),
	)
	if bad.Err() == nil {
		t.Fatal("short JWT secret must fail the graph")
	}
}
