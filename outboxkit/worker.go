package outboxkit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

type Worker struct {
	repo        Repository
	tr          Transport
	tx          pgkit.Transactor
	batchSize   int
	mode        PublishMode
	maxAttempts int
	tickTimeout time.Duration
	instr       *outboxInstruments
}

func NewWorker(instance string, repo Repository, tr Transport, tx pgkit.Transactor, cfg Config) *Worker {
	return &Worker{
		repo:        repo,
		tr:          tr,
		tx:          tx,
		batchSize:   cfg.BatchSize,
		mode:        cfg.PublishMode,
		maxAttempts: cfg.MaxAttempts,
		tickTimeout: cfg.TickTimeout,
		instr:       newOutboxInstruments(instance),
	}
}

type failedMsg struct {
	msg Message
	err error
}

// Run — тик в одной транзакции: запертые строки не видны другим репликам.
// Ошибка публикации возвращается после коммита — откат терял бы mark подтверждённых.
func (w *Worker) Run(ctx context.Context) error {
	if w.tickTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.tickTimeout)
		defer cancel()
	}
	start := time.Now()
	defer func() {
		w.instr.batchDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(w.instr.instance))
	}()

	var publishErr error
	err := w.tx.WithTx(ctx, func(ctx context.Context) error {
		var storeErr error
		publishErr, storeErr = w.tick(ctx)
		return storeErr
	})
	if errors.Is(err, ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	return publishErr
}

func (w *Worker) tick(ctx context.Context) (publishErr, storeErr error) {
	msgs, err := w.repo.ListUnpublished(ctx, w.batchSize, w.mode)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, err
		}
		return nil, fmt.Errorf("outboxkit worker: list: %w", err)
	}
	w.instr.batchSize.Record(ctx, int64(len(msgs)), metric.WithAttributes(w.instr.instance))
	if len(msgs) == 0 {
		return nil, nil
	}

	out := make([]OutMsg, len(msgs))
	for i, m := range msgs {
		out[i] = OutMsg{ID: m.ID, Subject: m.Subject, Payload: m.Payload}
	}

	var acked []Message
	var failed []failedMsg
	if w.mode == PublishOrdered {
		acked, failed, publishErr = w.publishOrdered(ctx, msgs, out)
	} else {
		acked, failed, publishErr = w.publishBatch(ctx, msgs, out)
	}

	if err := w.markAll(ctx, acked); err != nil {
		return publishErr, err
	}
	if err := w.recordFailures(ctx, failed); err != nil {
		return publishErr, err
	}
	return publishErr, nil
}

// publishBatch — async-батч: все улетают разом, метятся только подтверждённые
// PubAck'ом; неподтверждённые уходят на ретрай (дедуп по MsgID — без дублей).
func (w *Worker) publishBatch(ctx context.Context, msgs []Message, out []OutMsg) ([]Message, []failedMsg, error) {
	pubStart := time.Now()
	results, err := w.tr.PublishBatch(ctx, out)
	w.instr.publishDuration.Record(ctx, time.Since(pubStart).Seconds(), metric.WithAttributes(w.instr.instance))
	if err != nil {
		return nil, nil, fmt.Errorf("outboxkit worker: publish batch: %w", err)
	}

	var acked []Message
	var failed []failedMsg
	var firstErr error
	for i, m := range msgs {
		if perr := results[i]; perr != nil {
			w.processed(ctx, m.Subject, "publish_error")
			failed = append(failed, failedMsg{m, perr})
			if firstErr == nil {
				firstErr = fmt.Errorf("outboxkit worker: publish %s (id=%d): %w", m.Subject, m.ID, perr)
			}
			continue
		}
		acked = append(acked, m)
	}
	return acked, failed, firstErr
}

// publishOrdered — последовательная публикация со стопом на первой ошибке,
// чтобы более позднее событие не персистилось прежде неудавшегося.
func (w *Worker) publishOrdered(ctx context.Context, msgs []Message, out []OutMsg) ([]Message, []failedMsg, error) {
	pubStart := time.Now()
	defer func() {
		w.instr.publishDuration.Record(ctx, time.Since(pubStart).Seconds(), metric.WithAttributes(w.instr.instance))
	}()

	var acked []Message
	for i, m := range msgs {
		if err := w.tr.Publish(ctx, out[i]); err != nil {
			w.processed(ctx, m.Subject, "publish_error")
			return acked, []failedMsg{{m, err}}, fmt.Errorf("outboxkit worker: publish %s (id=%d): %w", m.Subject, m.ID, err)
		}
		acked = append(acked, m)
	}
	return acked, nil, nil
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

func (w *Worker) recordFailures(ctx context.Context, failed []failedMsg) error {
	if len(failed) == 0 {
		return nil
	}
	fs := make([]Failure, len(failed))
	for i, f := range failed {
		dead := w.maxAttempts > 0 && f.msg.Attempts+1 >= w.maxAttempts
		fs[i] = Failure{ID: f.msg.ID, Err: truncErr(f.err), Dead: dead}
		if dead {
			w.instr.deadTotal.Add(ctx, 1, metric.WithAttributes(w.instr.instance, attribute.String("subject", f.msg.Subject)))
		}
	}
	if err := w.repo.RecordFailures(ctx, fs); err != nil {
		return fmt.Errorf("outboxkit worker: record failures: %w", err)
	}
	return nil
}

func (w *Worker) processed(ctx context.Context, subject, status string) {
	w.instr.processedTotal.Add(ctx, 1, metric.WithAttributes(
		w.instr.instance,
		attribute.String("subject", subject),
		attribute.String("status", status),
	))
}

func truncErr(err error) string {
	const max = 1000
	s := err.Error()
	if len(s) > max {
		return s[:max]
	}
	return s
}
