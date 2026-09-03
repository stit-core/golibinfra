package outboxkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/testkit/txkit"
)

type fakeRepo struct {
	unpublished []outboxkit.Message
	marked      []int64
	failures    []outboxkit.Failure
	markErr     error
	listErr     error
	listMode    outboxkit.PublishMode
}

func (r *fakeRepo) Insert(context.Context, string, any) error { return nil }

func (r *fakeRepo) ListUnpublished(_ context.Context, limit int, mode outboxkit.PublishMode) ([]outboxkit.Message, error) {
	r.listMode = mode
	if r.listErr != nil {
		return nil, r.listErr
	}
	if limit < len(r.unpublished) {
		return r.unpublished[:limit], nil
	}
	return r.unpublished, nil
}

func (r *fakeRepo) MarkPublished(_ context.Context, ids []int64) error {
	if r.markErr != nil {
		return r.markErr
	}
	r.marked = append(r.marked, ids...)
	return nil
}

func (r *fakeRepo) RecordFailures(_ context.Context, failures []outboxkit.Failure) error {
	r.failures = append(r.failures, failures...)
	return nil
}

func deadIDs(fs []outboxkit.Failure) []int64 {
	var ids []int64
	for _, f := range fs {
		if f.Dead {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

type fakeTransport struct {
	published []string
	failOn    map[string]error
	batchErr  error
	deadline  bool
}

func (t *fakeTransport) Publish(ctx context.Context, m outboxkit.OutMsg) error {
	_, t.deadline = ctx.Deadline()
	if e, ok := t.failOn[m.Subject]; ok {
		return e
	}
	t.published = append(t.published, m.Subject)
	return nil
}

func (t *fakeTransport) PublishBatch(ctx context.Context, msgs []outboxkit.OutMsg) ([]error, error) {
	_, t.deadline = ctx.Deadline()
	if t.batchErr != nil {
		return nil, t.batchErr
	}
	errs := make([]error, len(msgs))
	for i, m := range msgs {
		if e, ok := t.failOn[m.Subject]; ok {
			errs[i] = e
			continue
		}
		t.published = append(t.published, m.Subject)
	}
	return errs, nil
}

func contains(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func newWorker(repo *fakeRepo, tr *fakeTransport, cfg outboxkit.Config) (*outboxkit.Worker, *txkit.Recorder) {
	tx := &txkit.Recorder{}
	return outboxkit.NewWorker("test", repo, tr, tx, cfg), tx
}

func TestWorkerPublishesAndMarksInOneTx(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{
		{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)},
		{ID: 2, Subject: "b", Payload: json.RawMessage(`{}`)},
	}}
	tr := &fakeTransport{}
	w, tx := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tr.published) != 2 {
		t.Fatalf("published = %d, want 2", len(tr.published))
	}
	if len(repo.marked) != 2 {
		t.Fatalf("marked = %d, want 2", len(repo.marked))
	}
	if tx.Calls() != 1 {
		t.Fatalf("tx calls = %d, want 1 (тик — одна транзакция)", tx.Calls())
	}
	if repo.listMode != outboxkit.PublishBatch {
		t.Fatalf("list mode = %q, want batch", repo.listMode)
	}
}

func TestWorkerBatchMarksOnlyAckedOnPartialFailure(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{
		{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)},
		{ID: 2, Subject: "boom", Payload: json.RawMessage(`{}`)},
		{ID: 3, Subject: "c", Payload: json.RawMessage(`{}`)},
	}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error on partial publish failure")
	}
	// id=2 (boom) не подтверждён → не помечен; 1 и 3 помечены.
	if got := repo.marked; len(got) != 2 || !contains(got, 1) || !contains(got, 3) || contains(got, 2) {
		t.Fatalf("marked = %v, want {1,3}", got)
	}
}

func TestWorkerOrderedStopsAtFirstFailure(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{
		{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)},
		{ID: 2, Subject: "boom", Payload: json.RawMessage(`{}`)},
		{ID: 3, Subject: "c", Payload: json.RawMessage(`{}`)},
	}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishOrdered})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error on ordered publish failure")
	}
	// Стоп на id=2: помечен только префикс {1}; id=3 не публиковался.
	if got := repo.marked; len(got) != 1 || !contains(got, 1) {
		t.Fatalf("marked = %v, want {1}", got)
	}
	if len(tr.published) != 1 || tr.published[0] != "a" {
		t.Fatalf("published = %v, want [a] (stop before id=3)", tr.published)
	}
	if repo.listMode != outboxkit.PublishOrdered {
		t.Fatalf("list mode = %q, want ordered", repo.listMode)
	}
}

func TestWorkerBusySkipsTick(t *testing.T) {
	repo := &fakeRepo{listErr: outboxkit.ErrBusy}
	tr := &fakeTransport{}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishOrdered})

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("ErrBusy must be a silent skip, got %v", err)
	}
	if len(tr.published) != 0 || len(repo.marked) != 0 {
		t.Fatal("busy tick must not publish or mark")
	}
}

func TestWorkerTickTimeoutBoundsPublish(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)}}}
	tr := &fakeTransport{}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, TickTimeout: time.Minute})

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !tr.deadline {
		t.Fatal("publish ctx has no deadline: OUTBOX_TICK_TIMEOUT not applied")
	}
}

func TestWorkerMarkErrorSurfaces(t *testing.T) {
	repo := &fakeRepo{
		unpublished: []outboxkit.Message{
			{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)},
		},
		markErr: errors.New("db down"),
	}
	tr := &fakeTransport{}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	// Публикация прошла, mark упал → ошибка наружу, строка остаётся unpublished
	// (переотправится; дедуп по MsgID убережёт от дубля в стриме).
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error when MarkPublished fails")
	}
	if len(repo.marked) != 0 {
		t.Fatalf("marked = %v, want none", repo.marked)
	}
}

func TestWorkerPoisonGoesDeadAfterMaxAttempts(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{
		{ID: 1, Subject: "boom", Payload: json.RawMessage(`{}`), Attempts: 1}, // след. попытка — 2-я
		{ID: 2, Subject: "young", Payload: json.RawMessage(`{}`), Attempts: 0},
	}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak"), "young": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, MaxAttempts: 2})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error on publish failure")
	}
	if len(repo.failures) != 2 {
		t.Fatalf("failures = %d, want 2", len(repo.failures))
	}
	// id=1 достиг 2 попыток → dead; id=2 (1-я попытка) — нет.
	if got := deadIDs(repo.failures); len(got) != 1 || got[0] != 1 {
		t.Fatalf("dead = %v, want {1}", got)
	}
}

func TestWorkerUnlimitedAttemptsNeverDead(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{
		{ID: 1, Subject: "boom", Payload: json.RawMessage(`{}`), Attempts: 99},
	}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, MaxAttempts: 0})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if got := deadIDs(repo.failures); len(got) != 0 {
		t.Fatalf("dead = %v, want none (MaxAttempts=0 → unlimited)", got)
	}
}

func TestWorkerBatchErrorMarksNothing(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{
		{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)},
	}}
	tr := &fakeTransport{batchErr: errors.New("jetstream disabled")}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected batch-level error")
	}
	if len(repo.marked) != 0 {
		t.Fatalf("marked = %v, want none", repo.marked)
	}
}
