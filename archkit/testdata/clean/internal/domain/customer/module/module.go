// Package module — fx-модуль домена customer.
package module

import (
	"go.uber.org/fx"

	"example.org/shop/internal/domain/customer"
	customerhttp "example.org/shop/internal/domain/customer/delivery/http"
	customerpg "example.org/shop/internal/domain/customer/infra/postgres"
)

// Module — провайдеры домена.
var Module = fx.Module(
	"customer",
	fx.Provide(
		customerpg.NewRepository,
		func(r *customerpg.Repository) customer.Repository { return r },
		customer.NewUsecase,
		func(uc *customer.Usecase) customerhttp.Usecase { return uc },
		customerhttp.NewHandler,
	),
)
