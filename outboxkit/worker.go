package outboxkit

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Worker struct {
	repo      Repository
	tr        Transport
	batchSize int
	mode      PublishMode
	instr     *outboxInstruments
}

func NewWorker(repo Repository, tr Transport, cfg Config) *Worker {
	return &Worker{
		repo:      repo,
		tr:        tr,
		batchSize: cfg.BatchSize,
		mode:      cfg.PublishMode,
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
	if len(msgs) == 0 {
		w.instr.batchDuration.Record(ctx, time.Since(start).Seconds())
		return nil
	}

	out := make([]OutMsg, len(msgs))
	for i, m := range msgs {
		out[i] = OutMsg{ID: m.ID, Subject: m.Subject, Payload: m.Payload}
	}

	var acked []Message
	var firstErr error
	if w.mode == PublishOrdered {
		acked, firstErr = w.publishOrdered(ctx, msgs, out)
	} else {
		acked, firstErr = w.publishBatch(ctx, msgs, out)
	}

	if err := w.markAll(ctx, acked); err != nil && firstErr == nil {
		firstErr = err
	}
	w.instr.batchDuration.Record(ctx, time.Since(start).Seconds())
	return firstErr
}

// publishBatch — async-батч: все улетают разом, метятся только подтверждённые
// PubAck'ом; неподтверждённые остаются на ретрай (дедуп по MsgID — без дублей).
func (w *Worker) publishBatch(ctx context.Context, msgs []Message, out []OutMsg) ([]Message, error) {
	pubStart := time.Now()
	results, err := w.tr.PublishBatch(ctx, out)
	w.instr.publishDuration.Record(ctx, time.Since(pubStart).Seconds())
	if err != nil {
		return nil, fmt.Errorf("outboxkit worker: publish batch: %w", err)
	}

	var acked []Message
	var firstErr error
	for i, m := range msgs {
		if results[i] != nil {
			w.processed(ctx, m.Subject, "publish_error")
			if firstErr == nil {
				firstErr = fmt.Errorf("outboxkit worker: publish %s (id=%d): %w", m.Subject, m.ID, results[i])
			}
			continue
		}
		acked = append(acked, m)
	}
	return acked, firstErr
}

// publishOrdered — последовательная публикация со стопом на первой ошибке,
// чтобы не персистить более позднее событие раньше неудавшегося.
func (w *Worker) publishOrdered(ctx context.Context, msgs []Message, out []OutMsg) ([]Message, error) {
	pubStart := time.Now()
	defer func() { w.instr.publishDuration.Record(ctx, time.Since(pubStart).Seconds()) }()

	var acked []Message
	for i, m := range msgs {
		if err := w.tr.Publish(ctx, out[i]); err != nil {
			w.processed(ctx, m.Subject, "publish_error")
			return acked, fmt.Errorf("outboxkit worker: publish %s (id=%d): %w", m.Subject, m.ID, err)
		}
		acked = append(acked, m)
	}
	return acked, nil
}

func (w *Worker) markAll(ctx context.Context, acked []Message) error {
	if len(acked) == 0 {
		return nil
	}
	ids := make([]int64, len(acked))
	for i, m := range acked {
		ids[i] = m.ID
	}
	if err := w.repo.MarkPublished(ctx, ids); err != nil {
		for _, m := range acked {
			w.processed(ctx, m.Subject, "mark_error")
		}
		return fmt.Errorf("outboxkit worker: mark published: %w", err)
	}
	for _, m := range acked {
		w.processed(ctx, m.Subject, "published")
	}
	return nil
}

func (w *Worker) processed(ctx context.Context, subject, status string) {
	w.instr.processedTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("subject", subject),
		attribute.String("status", status),
	))
}
