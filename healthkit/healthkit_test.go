package healthkit_test

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
)

func TestProvide_LandsInHealthGroup(t *testing.T) {
	type in struct {
		fx.In
		Probes []healthkit.Probe `group:"health"`
	}
	var got int
	app := fx.New(
		fx.NopLogger,
		healthkit.Provide(func() healthkit.Probe { return func(context.Context) error { return nil } }),
		healthkit.Provide(func() healthkit.Probe { return func(context.Context) error { return errors.New("down") } }),
		fx.Invoke(func(i in) { got = len(i.Probes) }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	if got != 2 {
		t.Fatalf("probes in group: got %d, want 2", got)
	}
}

func TestAll(t *testing.T) {
	ok := healthkit.Probe(func(context.Context) error { return nil })
	down := healthkit.Probe(func(context.Context) error { return errors.New("down") })

	if err := healthkit.All(context.Background(), ok, ok); err != nil {
		t.Fatalf("all ok: %v", err)
	}
	if err := healthkit.All(context.Background(), ok, down); err == nil {
		t.Fatal("want error from failing probe")
	}
}
