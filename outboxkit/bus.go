package outboxkit

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/natskit"
)

// Publisher — узкий порт публикации события: домены и тесты зависят от него, а
// не от конкретного *Bus.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

// Bus — insert-сторона outbox'а: сериализует payload и пишет его в Repository.
type Bus struct {
	repo       Repository
	instr      *outboxInstruments
	maxPayload int
	// subjects — WithSubjects инстанса; пусто — subject не проверяется.
	subjects []string
}

var _ Publisher = (*Bus)(nil)

// NewBus строит Bus поверх Repository; instance — значение label `instance`, предел тела —
// DefaultMaxPayloadBytes.
func NewBus(instance string, repo Repository) *Bus {
	return newBus(newOutboxInstruments(instance), repo, DefaultMaxPayloadBytes)
}

func newBus(instr *outboxInstruments, repo Repository, maxPayload int) *Bus {
	return &Bus{repo: repo, instr: instr, maxPayload: maxPayload}
}

// Publish сериализует payload в JSON и вставляет событие в outbox — вызывается
// внутри доменной транзакции, иначе теряется атомарность факта и его публикации.
// JSON больше предела — ErrPayloadTooLarge (брокер его не примет), subject вне
// WithSubjects инстанса — ErrSubjectNotDeclared (его стрим старт не проверял).
func (b *Bus) Publish(ctx context.Context, subject string, payload any) error {
	if len(b.subjects) > 0 && !slices.ContainsFunc(b.subjects, func(f string) bool { return natskit.SubjectMatches(f, subject) }) {
		b.inserted(ctx, subject, "undeclared_subject")
		return fmt.Errorf("%w: %s", ErrSubjectNotDeclared, subject)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		b.inserted(ctx, subject, "marshal_error")
		return fmt.Errorf("outboxkit: marshal payload: %w", err)
	}
	if len(data) > b.maxPayload {
		b.inserted(ctx, subject, "payload_too_large")
		return fmt.Errorf("%w: %s: %d bytes, limit %d", ErrPayloadTooLarge, subject, len(data), b.maxPayload)
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
