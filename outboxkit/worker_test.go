package outboxkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
)

func deadIDs(fs []outboxkit.Failure) []int64 {
	var ids []int64
	for _, f := range fs {
		if f.Dead {
			ids = append(ids, f.Ref.ID)
		}
	}
	return ids
}

func failedIDs(fs []outboxkit.Failure) []int64 {
	ids := make([]int64, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, f.Ref.ID)
	}
	return ids
}

func contains(ids []int64, want int64) bool {
	return slices.Contains(ids, want)
}

func msg(id int64, subject string) outboxkit.Message {
	return outboxkit.Message{
		ID:        id,
		CreatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(id) * time.Second),
		Subject:   subject,
		Payload:   json.RawMessage(`{}`),
	}
}

func newWorker(repo outboxkit.Repository, tr outboxkit.Transport, cfg outboxkit.Config) (*outboxkit.Worker, *pgtest.Recorder) {
	tx := &pgtest.Recorder{}
	return outboxkit.NewWorker("test", repo, tr, tx, cfg, logkit.Nop()), tx
}

func TestWorkerPublishesAndMarksInOneTx(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "a"), msg(2, "b")}}
	tr := &fakeTransport{}
	w, tx := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(tr.publishedSubjects()) != 2 {
		t.Fatalf("published = %d, want 2", len(tr.publishedSubjects()))
	}
	if len(repo.markedIDs()) != 2 {
		t.Fatalf("marked = %d, want 2", len(repo.markedIDs()))
	}
	if tx.Calls() != 1 {
		t.Fatalf("tx calls = %d, want 1 (тик — одна транзакция)", tx.Calls())
	}
	if repo.listMode != outboxkit.PublishBatch {
		t.Fatalf("list mode = %q, want batch", repo.listMode)
	}
	// MarkPublished получает ключ партиционирования, а не только id.
	if got := repo.marked[0].CreatedAt; got.IsZero() {
		t.Fatal("MessageRef.CreatedAt is zero — partition pruning key lost")
	}
}

func TestWorkerBatchMarksOnlyAckedOnPartialFailure(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "a"), msg(2, "boom"), msg(3, "c")}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error on partial publish failure")
	}
	// id=2 (boom) не подтверждён → не помечен; 1 и 3 помечены.
	if got := repo.markedIDs(); len(got) != 2 || !contains(got, 1) || !contains(got, 3) || contains(got, 2) {
		t.Fatalf("marked = %v, want {1,3}", got)
	}
}

func TestWorkerOrderedStopsAtFirstFailure(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "a"), msg(2, "boom"), msg(3, "c")}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishOrdered})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error on ordered publish failure")
	}
	// Стоп на id=2: помечен только префикс {1}; id=3 не публиковался.
	if got := repo.markedIDs(); len(got) != 1 || !contains(got, 1) {
		t.Fatalf("marked = %v, want {1}", got)
	}
	if pub := tr.publishedSubjects(); len(pub) != 1 || pub[0] != "a" {
		t.Fatalf("published = %v, want [a] (stop before id=3)", pub)
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
	if len(tr.publishedSubjects()) != 0 || len(repo.markedIDs()) != 0 {
		t.Fatal("busy tick must not publish or mark")
	}
}

func TestWorkerListErrorSurfaces(t *testing.T) {
	repo := &fakeRepo{listErr: errors.New("db down")}
	w, _ := newWorker(repo, &fakeTransport{}, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	err := w.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list") {
		t.Fatalf("err = %v, want list error", err)
	}
}

func TestWorkerTickTimeoutBoundsPublish(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "a")}}
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
		unpublished: []outboxkit.Message{msg(1, "a")},
		markErr:     errors.New("db down"),
	}
	tr := &fakeTransport{}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	// Публикация прошла, mark упал → ошибка наружу, строка остаётся unpublished
	// (переотправится; дедуп по MsgID убережёт от дубля в стриме).
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error when MarkPublished fails")
	}
	if len(repo.markedIDs()) != 0 {
		t.Fatalf("marked = %v, want none", repo.markedIDs())
	}
}

func TestWorkerRecordFailuresErrorSurfaces(t *testing.T) {
	repo := &fakeRepo{
		unpublished: []outboxkit.Message{msg(1, "boom")},
		recordErr:   errors.New("db down"),
	}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	err := w.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "record failures") {
		t.Fatalf("err = %v, want record failures error", err)
	}
}

func TestWorkerPoisonGoesDeadAfterMaxAttempts(t *testing.T) {
	first, second := msg(1, "boom"), msg(2, "young")
	first.Attempts = 1 // след. попытка — 2-я
	repo := &fakeRepo{unpublished: []outboxkit.Message{first, second}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak"), "young": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, MaxAttempts: 2})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error on publish failure")
	}
	fs := repo.recordedFailures()
	if len(fs) != 2 {
		t.Fatalf("failures = %d, want 2", len(fs))
	}
	// id=1 достиг 2 попыток → dead; id=2 (1-я попытка) — нет.
	if got := deadIDs(fs); len(got) != 1 || got[0] != 1 {
		t.Fatalf("dead = %v, want {1}", got)
	}
}

// MaxAttempts < 1 в литерале Config — бюджет по умолчанию, а не «без предела».
func TestWorkerZeroMaxAttemptsUsesDefault(t *testing.T) {
	last, young := msg(1, "boom"), msg(2, "boom")
	last.Attempts = outboxkit.DefaultMaxAttempts - 1
	young.Attempts = outboxkit.DefaultMaxAttempts - 2
	repo := &fakeRepo{unpublished: []outboxkit.Message{last, young}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, MaxAttempts: 0})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if got := deadIDs(repo.recordedFailures()); len(got) != 1 || got[0] != 1 {
		t.Fatalf("dead = %v, want {1} (MaxAttempts=0 → DefaultMaxAttempts)", got)
	}
}

// MaxAttempts=-1 — явный отказ от предела: строка не становится dead никогда.
func TestWorkerUnlimitedAttemptsNeverDead(t *testing.T) {
	m := msg(1, "boom")
	m.Attempts = 99
	repo := &fakeRepo{unpublished: []outboxkit.Message{m}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, MaxAttempts: -1})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if fs := repo.recordedFailures(); len(fs) != 1 || fs[0].Dead {
		t.Fatalf("failures = %+v, want one non-dead (MaxAttempts=-1 → unlimited)", fs)
	}
}

// Переход в dead — ERROR с координатами строки: без telemetry метрика — no-op, а событие
// больше не доставится.
func TestWorkerDeadIsLogged(t *testing.T) {
	m := msg(7, "order.created")
	m.Attempts = 2
	repo := &fakeRepo{unpublished: []outboxkit.Message{m}}
	tr := &fakeTransport{failOn: map[string]error{"order.created": errors.New("nak")}}
	log := logtest.Capture()
	w := outboxkit.NewWorker("orders", repo, tr, &pgtest.Recorder{},
		outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, MaxAttempts: 3}, log)

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected publish error")
	}
	e, ok := log.Find(slog.LevelError, "outboxkit: event dead")
	if !ok {
		t.Fatalf("no ERROR «outboxkit: event dead»: %+v", log.Entries())
	}
	want := map[string]string{"instance": "orders", "id": "7", "subject": "order.created", "attempts": "3", "err": "nak"}
	for k, v := range want {
		if got := e.Str(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// Сбой брокера целиком — не неудача строки: publish_error в метрике, а попытка не тратится —
// иначе простой брокера дольше бюджета увёл бы очередь в dead. Неудача строки рядом — тратит.
func TestWorkerBrokerDownSpendsNoAttempts(t *testing.T) {
	down := fmt.Errorf("%w: %w", outboxkit.ErrUnavailable, nats.ErrConnectionClosed)
	nak := errors.New("nak")
	cases := map[string]struct {
		mode       outboxkit.PublishMode
		tr         *fakeTransport
		wantFailed []int64
	}{
		"batch without results": {outboxkit.PublishBatch, &fakeTransport{batchErr: down}, nil},
		"batch per message":     {outboxkit.PublishBatch, &fakeTransport{failOn: map[string]error{"a": down, "b": down}}, nil},
		"batch mixed":           {outboxkit.PublishBatch, &fakeTransport{failOn: map[string]error{"a": down, "b": nak}}, []int64{2}},
		"ordered":               {outboxkit.PublishOrdered, &fakeTransport{failOn: map[string]error{"a": down}}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			collect := collectMetrics(t)
			repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "a"), msg(2, "b")}}
			w, _ := newWorker(repo, tc.tr, outboxkit.Config{BatchSize: 10, PublishMode: tc.mode, MaxAttempts: 1})

			if err := w.Run(context.Background()); !errors.Is(err, outboxkit.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if failed := failedIDs(repo.recordedFailures()); !slices.Equal(failed, tc.wantFailed) {
				t.Fatalf("failures = %v, want %v", failed, tc.wantFailed)
			}
			attrs := map[string]string{"instance": "test", "subject": "a", "status": "publish_error"}
			if got := counterValue(collect(), "outbox_events_processed_total", attrs); got != 1 {
				t.Fatalf("publish_error of the unavailable row = %d, want 1", got)
			}
		})
	}
}

func TestWorkerBatchErrorMarksNothing(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "a")}}
	tr := &fakeTransport{batchErr: errors.New("jetstream disabled")}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected batch-level error")
	}
	if len(repo.markedIDs()) != 0 {
		t.Fatalf("marked = %v, want none", repo.markedIDs())
	}
	if fs := repo.recordedFailures(); len(fs) != 0 {
		t.Fatalf("failures = %+v, want none: no per-row results", fs)
	}
}

// Неудача откладывает следующую попытку: без backoff строка ретраилась бы
// каждый тик и выжигала MaxAttempts за секунды недоступности брокера.
func TestWorkerBackoffGrowsWithAttempts(t *testing.T) {
	first, second, third := msg(1, "boom"), msg(2, "boom"), msg(3, "boom")
	second.Attempts = 3
	third.Attempts = 40 // насыщение потолком
	repo := &fakeRepo{unpublished: []outboxkit.Message{first, second, third}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{
		BatchSize:       10,
		PublishMode:     outboxkit.PublishBatch,
		RetryBackoff:    time.Second,
		RetryMaxBackoff: time.Minute,
	})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected publish error")
	}
	want := []time.Duration{time.Second, 8 * time.Second, time.Minute}
	fs := repo.recordedFailures()
	for i, f := range fs {
		if f.RetryAfter != want[i] {
			t.Fatalf("failure %d: RetryAfter = %v, want %v", i, f.RetryAfter, want[i])
		}
	}
}

func TestWorkerBackoffDisabled(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "boom")}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, RetryBackoff: 0})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected publish error")
	}
	if got := repo.recordedFailures()[0].RetryAfter; got != 0 {
		t.Fatalf("RetryAfter = %v, want 0 (backoff disabled)", got)
	}
}

// Zero-value Config не должен давать LIMIT 0 — тик молча ничего не публиковал бы.
func TestWorkerZeroConfigUsesDefaults(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "a")}}
	tr := &fakeTransport{}
	w, _ := newWorker(repo, tr, outboxkit.Config{})

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if repo.limit != outboxkit.DefaultBatchSize {
		t.Fatalf("limit = %d, want %d", repo.limit, outboxkit.DefaultBatchSize)
	}
	if repo.listMode != outboxkit.PublishBatch {
		t.Fatalf("list mode = %q, want batch", repo.listMode)
	}
	if len(repo.markedIDs()) != 1 {
		t.Fatal("zero-value Config published nothing")
	}
	// Тик идёт на context.WithoutCancel — без дедлайна его не оборвала бы и
	// остановка сервиса.
	if !repo.listHasDue {
		t.Fatal("zero-value Config оставил тик без дедлайна")
	}
	if left := time.Until(repo.listDue); left <= 0 || left > outboxkit.DefaultTickTimeout {
		t.Fatalf("дедлайн тика через %v, want (0, %v]", left, outboxkit.DefaultTickTimeout)
	}
}

// last_error режется по границе руны: обрывок многобайтового символа —
// невалидный UTF-8, который Postgres отвергает вместе со всем тиком.
func TestWorkerTruncatesErrorOnRuneBoundary(t *testing.T) {
	long := errors.New(strings.Repeat("ошибка ", 400)) // > 1000 рун, многобайтовые
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "boom")}}
	tr := &fakeTransport{failOn: map[string]error{"boom": long}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected publish error")
	}
	got := repo.recordedFailures()[0].Err
	if !utf8.ValidString(got) {
		t.Fatal("last_error is not valid UTF-8 — Postgres would reject the whole tick")
	}
	if n := utf8.RuneCountInString(got); n != 1000 {
		t.Fatalf("last_error = %d runes, want 1000", n)
	}
}

func TestWorkerKeepsInvalidUTF8Out(t *testing.T) {
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "boom")}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("bad \xff\xfe payload")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected publish error")
	}
	if got := repo.recordedFailures()[0].Err; !utf8.ValidString(got) {
		t.Fatalf("last_error %q is not valid UTF-8", got)
	}
}

// Остановка сервиса обрывает публикацию, но не фазу mark/commit: уже
// подтверждённые PubAck'ом строки должны дойти до published_at.
func TestWorkerMarksAfterContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "a")}}
	tr := &fakeTransport{beforePublish: cancel}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, TickTimeout: time.Minute})

	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(repo.markedIDs()) != 1 {
		t.Fatalf("marked = %v, want {1} despite cancelled ctx", repo.markedIDs())
	}
	if repo.markCtxErr != nil {
		t.Fatalf("mark ctx err = %v, want nil (mark runs on WithoutCancel)", repo.markCtxErr)
	}
}

// commitFailTx — Transactor, у которого коммит не удаётся после успешного fn.
type commitFailTx struct{ err error }

func (t commitFailTx) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := fn(pgkit.WithTestTx(ctx)); err != nil {
		return err
	}
	return t.err
}

func (t commitFailTx) WithTxOptions(ctx context.Context, _ pgx.TxOptions, fn func(ctx context.Context) error) error {
	return t.WithTx(ctx, fn)
}

// Счётчики состоявшихся исходов эмитятся после коммита: откат тика не должен
// оставлять published/dead в метриках.
func TestWorkerCountersOnlyAfterCommit(t *testing.T) {
	collect := collectMetrics(t)
	m := msg(1, "boom")
	m.Attempts = 1
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(2, "ok"), m}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	log := logtest.Capture()
	w := outboxkit.NewWorker("commitfail", repo, tr, commitFailTx{err: errors.New("commit failed")},
		outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, MaxAttempts: 2}, log)

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected commit error")
	}
	rm := collect()
	inst := map[string]string{"instance": "commitfail"}
	if got := counterValue(rm, "outbox_events_processed_total", map[string]string{"instance": "commitfail", "status": "published"}); got != 0 {
		t.Fatalf("published counter = %d after rollback, want 0", got)
	}
	if got := counterValue(rm, "outbox_events_dead_total", inst); got != 0 {
		t.Fatalf("dead counter = %d after rollback, want 0", got)
	}
	if log.Has(slog.LevelError, "outboxkit: event dead") {
		t.Fatal("dead logged although the tick rolled back")
	}
	if got := counterValue(rm, "outbox_events_processed_total", map[string]string{"instance": "commitfail", "status": "publish_error"}); got != 1 {
		t.Fatalf("publish_error counter = %d, want 1", got)
	}
}

func TestWorkerCountersAfterSuccessfulCommit(t *testing.T) {
	collect := collectMetrics(t)
	m := msg(1, "boom")
	m.Attempts = 1
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(2, "ok"), m}}
	tr := &fakeTransport{failOn: map[string]error{"boom": errors.New("nak")}}
	w, _ := newWorker(repo, tr, outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch, MaxAttempts: 2})

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected publish error after commit")
	}
	rm := collect()
	if got := counterValue(rm, "outbox_events_processed_total", map[string]string{"instance": "test", "status": "published"}); got != 1 {
		t.Fatalf("published counter = %d, want 1", got)
	}
	if got := counterValue(rm, "outbox_events_dead_total", map[string]string{"instance": "test"}); got != 1 {
		t.Fatalf("dead counter = %d, want 1", got)
	}
	for _, name := range []string{"outbox_batch_duration_seconds", "outbox_publish_duration_seconds"} {
		bounds := histogramBounds(rm, name)
		if len(bounds) == 0 || bounds[0] != 0.005 {
			t.Fatalf("%s has no shared duration buckets: %v", name, bounds)
		}
	}
}

func TestWorkerMarkErrorCounted(t *testing.T) {
	collect := collectMetrics(t)
	repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "a")}, markErr: errors.New("db down")}
	w := outboxkit.NewWorker("markfail", repo, &fakeTransport{}, &pgtest.Recorder{},
		outboxkit.Config{BatchSize: 10, PublishMode: outboxkit.PublishBatch}, logkit.Nop())

	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected mark error")
	}
	rm := collect()
	if got := counterValue(rm, "outbox_events_processed_total", map[string]string{"instance": "markfail", "status": "mark_error"}); got != 1 {
		t.Fatalf("mark_error counter = %d, want 1", got)
	}
}
