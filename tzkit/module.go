package tzkit

import "go.uber.org/fx"

// Module провайдит Zone из Config. Config регистрирует caller
// (`fx.Provide(tzkit.NewConfig)`) — kit'ы golibinfra свои Config'и во fx не кладут.
var Module = fx.Module(
	"tzkit",
	fx.Provide(NewZone),
)
