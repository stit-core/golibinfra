package outboxkit

import (
	"context"
	"fmt"

	"git.stit.tech/stit/golibinfra/natskit"
)

type Worker struct {
	repo      Repository
	bus       natskit.Bus
	batchSize int
}

func NewWorker(repo Repository, bus natskit.Bus, cfg Config) *Worker {
	return &Worker{repo: repo, bus: bus, batchSize: cfg.BatchSize}
}

func (w *Worker) Run(ctx context.Context) error {
	msgs, err := w.repo.ListUnpublished(ctx, w.batchSize)
	if err != nil {
		return fmt.Errorf("outboxkit worker: list: %w", err)
	}
	for _, msg := range msgs {
		if err := w.bus.Publish(ctx, msg.Subject, msg.Payload); err != nil {
			return fmt.Errorf("outboxkit worker: publish %s: %w", msg.Subject, err)
		}
		if err := w.repo.MarkPublished(ctx, msg.ID); err != nil {
			return fmt.Errorf("outboxkit worker: mark published: %w", err)
		}
	}
	return nil
}
