package outboxkit

import (
	"context"

	"git.stit.tech/akadmin/golibinfra/natskit"
)

// Publisher — узкий port-интерфейс для публикации событий, чтобы домены
// и тесты зависели от него, а не от конкретного *Bus.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

type Bus struct {
	repo    Repository
	natsBus natskit.Bus
}

var _ Publisher = (*Bus)(nil)

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
