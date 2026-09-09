package golibinfra_test

import (
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/telemetry"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

func TestReadmeFlagshipExample(t *testing.T) {
	app := fx.ValidateApp(
		fx.Provide(
			telemetry.NewConfig, logkit.NewConfig, httpkit.NewConfig,
			pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig, natskit.NewConfig,
			cachekit.NewConfig, outboxkit.NewConfig, pgstore.NewConfig,
		),
		telemetry.Module,
		logkit.Module,
		httpkit.Module,
		pgkit.Module,
		pgkit.MigrateModule,
		natskit.Module(""),
		pgstore.Module,
		outboxkit.Module,
		cachekit.Module,
		timerkit.Module,
		healthkit.Provide(pgkit.Probe),
		healthkit.Provide(natskit.Probe),
		healthkit.Provide(cachekit.Probe),
	)
	if app != nil {
		t.Fatalf("ValidateApp: %v", app)
	}
}
