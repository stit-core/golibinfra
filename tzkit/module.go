package tzkit

import "go.uber.org/fx"

// Module провайдит Zone из Config. Config регистрирует caller
// (`fx.Provide(tzkit.NewConfig)`) — kit'ы golibinfra свои Config'и во fx не кладут. Zone строится
// на старте и без потребителя: пустая или неизвестная APP_TZ валит запуск.
var Module = fx.Module(
	"tzkit",
	fx.Provide(NewZone),
	// Без Invoke конструктор ленив: проверка APP_TZ ждала бы первого потребителя Zone.
	fx.Invoke(func(Zone) {}),
)
