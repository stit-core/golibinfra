// Package outbox — outbox домена order: своя таблица, свои миграции.
package outbox

import (
	"git.stit.tech/stit-core/golibinfra/pgkit"

	"example.org/shop/internal/platform/events"
)

// Providers — outbox домена в графе.
var Providers = events.OutboxFor("order")

// Migrations — источник миграций таблицы outbox домена.
var Migrations pgkit.Source
