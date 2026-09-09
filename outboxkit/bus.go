package outboxkit

import (
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Publisher — узкий порт публикации события: домены и тесты зависят от него, а
// не от конкретного *Bus.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

// Bus — insert-сторона outbox'а: сериализует payload и пишет его в Repository.
type Bus struct {
	repo  Repository
	instr *outboxInstruments
}

var _ Publisher = (*Bus)(nil)

// NewBus строит Bus поверх Repository; instance — значение label `instance`.
func NewBus(instance string, repo Repository) *Bus {
	return newBus(newOutboxInstruments(instance), repo)
}

func newBus(instr *outboxInstruments, repo Repository) *Bus {
	return &Bus{repo: repo, instr: instr}
}

// Publish сериализует payload в JSON и вставляет событие в outbox — вызывается
// внутри доменной транзакции, иначе теряется атомарность факта и его публикации.
func (b *Bus) Publish(ctx context.Context, subject string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		b.inserted(ctx, subject, "marshal_error")
		return fmt.Errorf("outboxkit: marshal payload: %w", err)
	}
	status := "ok"
	if err = b.repo.Insert(ctx, subject, data); err != nil {
		status = "error"
	}
	b.inserted(ctx, subject, status)
	return err
}

func (b *Bus) inserted(ctx context.Context, subject, status string) {
	b.instr.insertedTotal.Add(ctx, 1, metric.WithAttributes(
		b.instr.instance,
		attribute.String("subject", subject),
		attribute.String("status", status),
	))
}
