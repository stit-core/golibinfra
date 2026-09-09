package outboxkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// commitGrace — запас времени поверх TickTimeout на фазу mark/commit: она идёт
// на context.WithoutCancel, чтобы остановка сервиса не откатывала уже
// подтверждённые PubAck'ом факты.
const commitGrace = 5 * time.Second

// maxLastError — верхняя граница текста ошибки в last_error (в рунах).
const maxLastError = 1000

// Worker — drain-сторона outbox'а: тик = транзакция list → publish → mark.
type Worker struct {
	repo            Repository
	tr              Transport
	tx              pgkit.Transactor
	batchSize       int
	mode            PublishMode
	maxAttempts     int
	tickTimeout     time.Duration
	retryBackoff    time.Duration
	retryMaxBackoff time.Duration
	instr           *outboxInstruments
}

// NewWorker строит Worker; instance — значение label `instance`. Невалидные
// поля cfg заменяются дефолтами (Config.Validate ловит их на старте).
func NewWorker(instance string, repo Repository, tr Transport, tx pgkit.Transactor, cfg Config) *Worker {
	return newWorker(newOutboxInstruments(instance), repo, tr, tx, cfg)
}

func newWorker(instr *outboxInstruments, repo Repository, tr Transport, tx pgkit.Transactor, cfg Config) *Worker {
	cfg = cfg.withDefaults()
	return &Worker{
		repo:            repo,
		tr:              tr,
		tx:              tx,
		batchSize:       cfg.BatchSize,
		mode:            cfg.PublishMode,
		maxAttempts:     cfg.MaxAttempts,
		tickTimeout:     cfg.TickTimeout,
		retryBackoff:    cfg.RetryBackoff,
		retryMaxBackoff: cfg.RetryMaxBackoff,
		instr:           instr,
	}
}

type failedMsg struct {
	msg Message
	err error
}

// tickResult — исходы тика, которые становятся метриками только после коммита:
// до него транзакция может откатиться и факты не случатся.
type tickResult struct {
	published []Message
	dead      []Message
}

// Run — тик в одной транзакции: запертые строки не видны другим репликам.
// Ошибка публикации возвращается после коммита — откат терял бы mark подтверждённых.
func (w *Worker) Run(ctx context.Context) error {
	// Транзакция живёт на неотменяемом ctx: отмена на остановке сервиса не
	// должна откатывать фазу mark/commit уже подтверждённых PubAck'ом строк.
	txCtx, cancelTx := context.WithTimeout(context.WithoutCancel(ctx), w.tickTimeout+commitGrace)
	defer cancelTx()

	start := time.Now()
	defer func() {
		w.instr.batchDuration.Record(txCtx, time.Since(start).Seconds(), metric.WithAttributes(w.instr.instance))
	}()

	var res tickResult
	var publishErr error
	err := w.tx.WithTx(txCtx, func(txCtx context.Context) error {
		opCtx, cancelOp := w.opContext(ctx, txCtx)
		defer cancelOp()

		var storeErr error
		res, publishErr, storeErr = w.tick(opCtx, txCtx)
		return storeErr
	})
	if errors.Is(err, ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	w.recordCommitted(txCtx, res)
	return publishErr
}

// opContext — ctx фаз list/publish: несёт транзакцию из txCtx, но обрывается
// вместе с ctx вызывающего и по дедлайну тика.
func (w *Worker) opContext(ctx, txCtx context.Context) (context.Context, context.CancelFunc) {
	opCtx, cancel := context.WithTimeout(txCtx, w.tickTimeout)
	stop := context.AfterFunc(ctx, cancel)
	return opCtx, func() {
		stop()
		cancel()
	}
}

// tick: opCtx ограничен дедлайном тика и отменой вызывающего (list, publish),
// txCtx переживает отмену (mark, record failures).
func (w *Worker) tick(opCtx, txCtx context.Context) (res tickResult, publishErr, storeErr error) {
	msgs, err := w.repo.ListUnpublished(opCtx, w.batchSize, w.mode)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return res, nil, err
		}
		return res, nil, fmt.Errorf("outboxkit worker: list: %w", err)
	}
	w.instr.batchSize.Record(opCtx, int64(len(msgs)), metric.WithAttributes(w.instr.instance))
	if len(msgs) == 0 {
		return res, nil, nil
	}

	out := make([]OutMsg, len(msgs))
	for i, m := range msgs {
		out[i] = OutMsg{ID: m.ID, Subject: m.Subject, Payload: m.Payload}
	}

	var acked []Message
	var failed []failedMsg
	if w.mode == PublishOrdered {
		acked, failed, publishErr = w.publishOrdered(opCtx, msgs, out)
	} else {
		acked, failed, publishErr = w.publishBatch(opCtx, msgs, out)
	}

	if err := w.markAll(txCtx, acked); err != nil {
		return res, publishErr, err
	}
	dead, err := w.recordFailures(txCtx, failed)
	if err != nil {
		return res, publishErr, err
	}
	return tickResult{published: acked, dead: dead}, publishErr, nil
}

// publishBatch — async-батч: все улетают разом, метятся только подтверждённые
// PubAck'ом; неподтверждённые уходят на ретрай (дедуп по MsgID — без дублей).
func (w *Worker) publishBatch(ctx context.Context, msgs []Message, out []OutMsg) ([]Message, []failedMsg, error) {
	pubStart := time.Now()
	results, err := w.tr.PublishBatch(ctx, out)
	w.instr.publishDuration.Record(ctx, time.Since(pubStart).Seconds(), metric.WithAttributes(w.instr.instance))
	// Транспорт отдаёт results и при ошибке батча (отмена ctx): подтверждённые
	// PubAck'и там уже есть, отбрасывать их — повторная публикация.
	if len(results) != len(msgs) {
		if err == nil {
			err = fmt.Errorf("outboxkit worker: transport returned %d results for %d messages", len(results), len(msgs))
		}
		return nil, nil, fmt.Errorf("outboxkit worker: publish batch: %w", err)
	}

	var acked []Message
	var failed []failedMsg
	firstErr := err
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
	refs := make([]MessageRef, len(acked))
	for i, m := range acked {
		refs[i] = m.Ref()
	}
	if err := w.repo.MarkPublished(ctx, refs); err != nil {
		for _, m := range acked {
			w.processed(ctx, m.Subject, "mark_error")
		}
		return fmt.Errorf("outboxkit worker: mark published: %w", err)
	}
	return nil
}

func (w *Worker) recordFailures(ctx context.Context, failed []failedMsg) ([]Message, error) {
	if len(failed) == 0 {
		return nil, nil
	}
	fs := make([]Failure, len(failed))
	var dead []Message
	for i, f := range failed {
		isDead := w.maxAttempts > 0 && f.msg.Attempts+1 >= w.maxAttempts
		fs[i] = Failure{
			Ref:        f.msg.Ref(),
			Err:        truncErr(f.err),
			RetryAfter: w.retryAfter(f.msg.Attempts),
			Dead:       isDead,
		}
		if isDead {
			dead = append(dead, f.msg)
		}
	}
	if err := w.repo.RecordFailures(ctx, fs); err != nil {
		return nil, fmt.Errorf("outboxkit worker: record failures: %w", err)
	}
	return dead, nil
}

// retryAfter — экспоненциальный backoff от числа уже случившихся попыток:
// без него неудачная строка ретраится каждый тик и выжигает MaxAttempts за
// секунды недоступности брокера.
func (w *Worker) retryAfter(attempts int) time.Duration {
	if w.retryBackoff <= 0 {
		return 0
	}
	d := w.retryBackoff
	for range attempts {
		if w.retryMaxBackoff > 0 && d >= w.retryMaxBackoff {
			return w.retryMaxBackoff
		}
		if d > time.Duration(1<<62) {
			break
		}
		d *= 2
	}
	if w.retryMaxBackoff > 0 && d > w.retryMaxBackoff {
		return w.retryMaxBackoff
	}
	return d
}

// recordCommitted эмитит счётчики состоявшихся исходов — после коммита тика.
func (w *Worker) recordCommitted(ctx context.Context, res tickResult) {
	for _, m := range res.published {
		w.processed(ctx, m.Subject, "published")
	}
	for _, m := range res.dead {
		w.instr.deadTotal.Add(ctx, 1, metric.WithAttributes(
			w.instr.instance,
			attribute.String("subject", m.Subject),
		))
	}
}

func (w *Worker) processed(ctx context.Context, subject, status string) {
	w.instr.processedTotal.Add(ctx, 1, metric.WithAttributes(
		w.instr.instance,
		attribute.String("subject", subject),
		attribute.String("status", status),
	))
}

// truncErr режет текст по границе руны: обрывок многобайтового символа — это
// невалидный UTF-8, который Postgres отвергает вместе со всем тиком.
func truncErr(err error) string {
	s := strings.ToValidUTF8(err.Error(), "")
	if utf8.RuneCountInString(s) <= maxLastError {
		return s
	}
	n := 0
	for i := range s {
		if n == maxLastError {
			return s[:i]
		}
		n++
	}
	return s
}
