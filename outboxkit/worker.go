package outboxkit

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit/golibinfra/natskit"
)

type Worker struct {
	repo      Repository
	bus       natskit.Bus
	batchSize int
	instr     *outboxInstruments
}

func NewWorker(repo Repository, bus natskit.Bus, cfg Config) *Worker {
	return &Worker{
		repo:      repo,
		bus:       bus,
		batchSize: cfg.BatchSize,
		instr:     newOutboxInstruments(),
	}
}

func (w *Worker) Run(ctx context.Context) error {
	start := time.Now()
	msgs, err := w.repo.ListUnpublished(ctx, w.batchSize)
	if err != nil {
		w.instr.batchDuration.Record(ctx, time.Since(start).Seconds())
		return fmt.Errorf("outboxkit worker: list: %w", err)
	}
	w.instr.batchSize.Record(ctx, int64(len(msgs)))

	for _, msg := range msgs {
		pubStart := time.Now()
		subjAttr := metric.WithAttributes(attribute.String("subject", msg.Subject))

		if err := w.bus.Publish(ctx, msg.Subject, msg.Payload); err != nil {
			w.instr.processedTotal.Add(ctx, 1, metric.WithAttributes(
				attribute.String("subject", msg.Subject),
				attribute.String("status", "publish_error"),
			))
			w.instr.publishDuration.Record(ctx, time.Since(pubStart).Seconds(), subjAttr)
			w.instr.batchDuration.Record(ctx, time.Since(start).Seconds())
			return fmt.Errorf("outboxkit worker: publish %s: %w", msg.Subject, err)
		}
		if err := w.repo.MarkPublished(ctx, msg.ID); err != nil {
			w.instr.processedTotal.Add(ctx, 1, metric.WithAttributes(
				attribute.String("subject", msg.Subject),
				attribute.String("status", "mark_error"),
			))
			w.instr.publishDuration.Record(ctx, time.Since(pubStart).Seconds(), subjAttr)
			w.instr.batchDuration.Record(ctx, time.Since(start).Seconds())
			return fmt.Errorf("outboxkit worker: mark published: %w", err)
		}
		w.instr.processedTotal.Add(ctx, 1, metric.WithAttributes(
			attribute.String("subject", msg.Subject),
			attribute.String("status", "published"),
		))
		w.instr.publishDuration.Record(ctx, time.Since(pubStart).Seconds(), subjAttr)
	}
	w.instr.batchDuration.Record(ctx, time.Since(start).Seconds())
	return nil
}
