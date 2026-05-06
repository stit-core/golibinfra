package outboxkit

import (
	"context"

	"git.stit.tech/akadmin/golibinfra/natskit"
)

type Bus struct {
	repo    Repository
	natsBus natskit.Bus
}

func NewBus(repo Repository, natsBus natskit.Bus) *Bus {
	return &Bus{repo: repo, natsBus: natsBus}
}

func (b *Bus) Publish(ctx context.Context, subject string, payload any) error {
	return b.repo.Insert(ctx, subject, payload)
}

func (b *Bus) Subscribe(ctx context.Context, subject string, handler func(ctx context.Context, payload []byte) error) error {
	return b.natsBus.Subscribe(ctx, subject, handler)
}

func PublishJSON[T any](b *Bus, ctx context.Context, subject string, msg T) error {
	return b.Publish(ctx, subject, msg)
}
