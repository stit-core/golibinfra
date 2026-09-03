// Package healthkit — общий тип health-пробы для fx-группы `health`:
// киты (pgkit, natskit, cachekit) возвращают Probe, httpkit собирает их в /ready.
package healthkit

import (
	"context"

	"go.uber.org/fx"
)

// Probe — проверка готовности зависимости. Именованный тип обязателен: dig
// ключует группу парой «имя + тип», безымянный func(ctx) error в группу не попадает.
type Probe func(ctx context.Context) error

// Group — имя fx-группы, из которой httpkit читает пробы.
const Group = "health"

// Provide регистрирует конструктор, возвращающий Probe, в группу `health`.
func Provide(ctor any) fx.Option {
	return fx.Provide(fx.Annotate(ctor, fx.ResultTags(`group:"`+Group+`"`)))
}

// All возвращает первую ошибку среди проб или nil.
func All(ctx context.Context, probes ...Probe) error {
	for _, p := range probes {
		if err := p(ctx); err != nil {
			return err
		}
	}
	return nil
}
