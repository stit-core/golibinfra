// Package events — outbox домена поверх outboxkit.
package events

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
)

// OutboxFor — провайдеры outbox домена: своя таблица и свой воркер.
func OutboxFor(domain string) fx.Option {
	return fx.Options(pgstore.FactoryModule, outboxkit.Instance(domain, "outbox_"+domain))
}
