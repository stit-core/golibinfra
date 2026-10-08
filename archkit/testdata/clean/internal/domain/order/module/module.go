// Package module — fx-модуль домена order.
package module

import (
	"go.uber.org/fx"

	"example.org/shop/internal/domain/order"
	orderhttp "example.org/shop/internal/domain/order/delivery/http"
	orderoutbox "example.org/shop/internal/domain/order/infra/outbox"
	orderpg "example.org/shop/internal/domain/order/infra/postgres"
)

// Module — провайдеры домена.
var Module = fx.Module(
	"order",
	orderoutbox.Providers,
	fx.Provide(
		orderpg.NewRepository,
		func(r *orderpg.Repository) order.Repository { return r },
		newCustomerReader,
		order.NewUsecase,
		func(uc *order.Usecase) (orderhttp.Usecase, error) { return uc, nil },
		orderhttp.NewHandler,
	),
)
