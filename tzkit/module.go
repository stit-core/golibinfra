package tzkit

import "go.uber.org/fx"

// Module провайдит Zone из Config. Config регистрирует caller — kit'ы
// golibinfra свои Config'и во fx не кладут.
var Module = fx.Module("tzkit",
	fx.Provide(NewZone),
)
