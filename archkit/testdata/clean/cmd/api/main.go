package main

import (
	"go.uber.org/fx"

	customerhttp "example.org/shop/internal/domain/customer/delivery/http"
	customermod "example.org/shop/internal/domain/customer/module"
	orderhttp "example.org/shop/internal/domain/order/delivery/http"
	ordermod "example.org/shop/internal/domain/order/module"
)

func main() {
	fx.New(
		customermod.Module,
		ordermod.Module,
		fx.Invoke(customerhttp.Register, orderhttp.Register),
	).Run()
}
