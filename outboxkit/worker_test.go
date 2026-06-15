package outboxkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
)

type fakeRepo struct {
	unpublished []outboxkit.Message
	marked      []int64
	markErr     error
}

func (r *fakeRepo) Insert(context.Context, string, any) error { return nil }

func (r *fakeRepo) ListUnpublished(_ context.Context, limit int) ([]outboxkit.Message, error) {
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

type fakeTransport struct {
	published []string
	failOn    map[string]error
	batchErr  error
}

func (t *fakeTransport) Publish(_ context.Context, m outboxkit.OutMsg) error {
	if e, ok := t.failOn[m.Subject]; ok {
		return e
	}
	t.published = append(t.published, m.Subject)
	return nil
}

func (t *fakeTransport) PublishBatch(_ context.Context, msgs []outboxkit.OutMsg) ([]error, error) {
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

func TestWorkerPublishesAndMarks(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{
		{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)},
		{ID: 2, Subject: "b", Payload: json.RawMessage(`{}`)},
	}}
	tr := &fakeTransport{}
	w := outboxkit.NewWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tr.published) != 2 {
		t.Fatalf("published = %d, want 2", len(tr.published))
	}
	if len(repo.marked) != 2 {
		t.Fatalf("marked = %d, want 2", len(repo.marked))
	}
}

func TestWorkerBatchMarksOnlyAckedOnPartialFailure(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{
		{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)},
		{ID: 2, Subject: "boom", Payload: json.RawMessage(`{}`)},
		{ID: 3, Subject: "c", Payload: json.RawMessage(`{}`)},
	}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w := outboxkit.NewWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

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
	w := outboxkit.NewWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishOrdered})

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
}

func TestWorkerMarkErrorSurfaces(t *testing.T) {
	repo := &fakeRepo{
		unpublished: []outboxkit.Message{
			{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)},
		},
		markErr: errors.New("db down"),
	}
	tr := &fakeTransport{}
	w := outboxkit.NewWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	// Публикация прошла, mark упал → ошибка наружу, строка остаётся unpublished
	// (переотправится; дедуп по MsgID убережёт от дубля в стриме).
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error when MarkPublished fails")
	}
	if len(repo.marked) != 0 {
		t.Fatalf("marked = %v, want none", repo.marked)
	}
}

func TestWorkerBatchErrorMarksNothing(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{
		{ID: 1, Subject: "a", Payload: json.RawMessage(`{}`)},
	}}
	tr := &fakeTransport{batchErr: errors.New("jetstream disabled")}
	w := outboxkit.NewWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected batch-level error")
	}
	if len(repo.marked) != 0 {
		t.Fatalf("marked = %v, want none", repo.marked)
	}
}
