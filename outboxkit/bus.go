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

func NewBus(instance string, repo Repository) *Bus {
	return &Bus{repo: repo, instr: newOutboxInstruments(instance)}
}

func (b *Bus) Publish(ctx context.Context, subject string, payload any) error {
	status := "ok"
	err := b.repo.Insert(ctx, subject, payload)
	if err != nil {
		status = "error"
	}
	b.instr.insertedTotal.Add(ctx, 1, metric.WithAttributes(
		b.instr.instance,
		attribute.String("subject", subject),
		attribute.String("status", status),
	))
	return err
}
