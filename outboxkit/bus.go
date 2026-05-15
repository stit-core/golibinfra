package outboxkit

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-infra/golibinfra/natskit"
)

// Publisher — узкий port-интерфейс для публикации событий, чтобы домены
// и тесты зависели от него, а не от конкретного *Bus.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

type Bus struct {
	repo    Repository
	natsBus natskit.Bus
	instr   *outboxInstruments
}

var _ Publisher = (*Bus)(nil)

func NewBus(repo Repository, natsBus natskit.Bus) *Bus {
	return &Bus{repo: repo, natsBus: natsBus, instr: newOutboxInstruments()}
}

func (b *Bus) Publish(ctx context.Context, subject string, payload any) error {
	if err := b.repo.Insert(ctx, subject, payload); err != nil {
		b.instr.insertedTotal.Add(ctx, 1, metric.WithAttributes(
			attribute.String("subject", subject),
			attribute.String("status", "error"),
		))
		return err
	}
	b.instr.insertedTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("subject", subject),
		attribute.String("status", "ok"),
	))
	return nil
}

func (b *Bus) Subscribe(ctx context.Context, subject string, handler func(ctx context.Context, payload []byte) error) error {
	return b.natsBus.Subscribe(ctx, subject, handler)
}

func PublishJSON[T any](b *Bus, ctx context.Context, subject string, msg T) error {
	return b.Publish(ctx, subject, msg)
}
