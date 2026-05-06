package outboxkit

import (
	"context"

	"git.stit.tech/akadmin/golibinfra/natskit"
)

// Bus is a transactional outbox bus: Publish writes the message to the outbox
// table inside the caller's transaction (via pgkit.DB tx-aware Exec). The
// background Worker drains the outbox and forwards to the underlying NATS bus.
type Bus struct {
	repo    Repository
	natsBus natskit.Bus
}

func NewBus(repo Repository, natsBus natskit.Bus) *Bus {
	return &Bus{repo: repo, natsBus: natsBus}
}

// Publish stores the message in the outbox. It MUST be called inside the same
// transaction as the business write — on rollback, the message disappears too.
func (b *Bus) Publish(ctx context.Context, subject string, payload any) error {
	return b.repo.Insert(ctx, subject, payload)
}

// Subscribe is a thin pass-through to the underlying NATS bus.
func (b *Bus) Subscribe(ctx context.Context, subject string, handler func(ctx context.Context, payload []byte) error) error {
	return b.natsBus.Subscribe(ctx, subject, handler)
}

// PublishJSON is a typed helper preserving compile-time type safety on the call site.
func PublishJSON[T any](b *Bus, ctx context.Context, subject string, msg T) error {
	return b.Publish(ctx, subject, msg)
}
