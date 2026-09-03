package natskit_test

import (
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestModule_DefaultAndNamedInstancesCoexist(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(func() logkit.Logger { return logkit.Nop() }),
		natskit.Module(""),
		natskit.Module("hub"),
		natskit.HealthProbe(""),
		fx.Invoke(fx.Annotate(
			func(local, hub natskit.StreamBus, hubBus natskit.Bus, hubClient *natskit.Client) {},
			fx.ParamTags(``, `name:"hub"`, `name:"hub"`, `name:"hub"`),
		)),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

func TestModule_NamedInstanceHealthProbe(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(func() logkit.Logger { return logkit.Nop() }),
		natskit.Module("hub"),
		natskit.HealthProbe("hub"),
		fx.Invoke(fx.Annotate(
			func(probes []healthkit.Probe) {},
			fx.ParamTags(`group:"`+healthkit.Group+`"`),
		)),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// Probe совместима с healthkit.Provide — путь, которым пробы регистрируют остальные киты.
func TestModule_ProbeViaHealthkitProvide(t *testing.T) {
	err := fx.ValidateApp(
		fx.Provide(func() logkit.Logger { return logkit.Nop() }),
		natskit.Module(""),
		healthkit.Provide(natskit.Probe),
		fx.Invoke(fx.Annotate(
			func(probes []healthkit.Probe) {},
			fx.ParamTags(`group:"`+healthkit.Group+`"`),
		)),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}
