package main

import (
	"git.stit.tech/stit-core/golibinfra/pgkit"

	orderoutbox "example.org/shop/internal/domain/order/infra/outbox"
)

var sources = []pgkit.Source{orderoutbox.Migrations}

func main() { _ = sources }
