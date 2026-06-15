package outboxkit

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Publisher — узкий port-интерфейс для публикации событий, чтобы домены
// и тесты зависели от него, а не от конкретного *Bus.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

type Bus struct {
	repo  Repository
	instr *outboxInstruments
}

var _ Publisher = (*Bus)(nil)

func NewBus(repo Repository) *Bus {
	return &Bus{repo: repo, instr: newOutboxInstruments()}
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

func PublishJSON[T any](b *Bus, ctx context.Context, subject string, msg T) error {
	return b.Publish(ctx, subject, msg)
}
