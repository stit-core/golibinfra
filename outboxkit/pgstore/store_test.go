package pgstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

type sample struct {
	Foo string `json:"foo"`
	N   int    `json:"n"`
}

func payload(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// startFixture поднимает Postgres с миграциями кита.
func startFixture(t *testing.T, opts ...testkit.Option) (*testkit.PostgresFixture, context.Context) {
	t.Helper()
	opts = append([]testkit.Option{testkit.WithMigrations(pgstore.Migrations)}, opts...)
	fix := testkit.StartPostgres(t, opts...)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return fix, ctx
}

// startStore отдаёт DB + Transactor на одном пуле фикстуры.
func startStore(t *testing.T, opts ...testkit.Option) (*pgkit.DB, pgkit.Transactor, context.Context) {
	t.Helper()
	fix, ctx := startFixture(t, opts...)
	return fix.DB(t), fix.Transactor(t), ctx
}

// list читает батч в собственной транзакции (как это делает Worker).
func list(t *testing.T, tr pgkit.Transactor, s *pgstore.Store, ctx context.Context, limit int) []outboxkit.Message {
	t.Helper()
	return listMode(t, tr, s, ctx, limit, outboxkit.PublishBatch)
}

func listMode(t *testing.T, tr pgkit.Transactor, s *pgstore.Store, ctx context.Context, limit int, mode outboxkit.PublishMode) []outboxkit.Message {
	t.Helper()
	var msgs []outboxkit.Message
	err := tr.WithTx(ctx, func(ctx context.Context) error {
		var err error
		msgs, err = s.ListUnpublished(ctx, limit, mode)
		return err
	})
	if err != nil {
		t.Fatalf("ListUnpublished: %v", err)
	}
	return msgs
}

func refs(msgs ...outboxkit.Message) []outboxkit.MessageRef {
	out := make([]outboxkit.MessageRef, len(msgs))
	for i, m := range msgs {
		out[i] = m.Ref()
	}
	return out
}

func TestStore_RequireTx_NoTxIsErrNoTx(t *testing.T) {
	s := pgstore.NewStore(nil, pgstore.WithRequireTx())
	err := s.Insert(context.Background(), "evt", payload(t, sample{N: 1}))
	if !errors.Is(err, outboxkit.ErrNoTx) {
		t.Fatalf("err = %v, want ErrNoTx", err)
	}
}

func TestStore_Insert_EmptyPayload(t *testing.T) {
	s := pgstore.NewStore(nil)
	if err := s.Insert(context.Background(), "evt", nil); err == nil {
		t.Fatal("expected error on empty payload")
	}
}

func TestStore_RequireTx_WithinTx(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db, pgstore.WithRequireTx())

	if err := s.Insert(ctx, "evt", payload(t, sample{N: 1})); !errors.Is(err, outboxkit.ErrNoTx) {
		t.Fatalf("err = %v, want ErrNoTx outside tx", err)
	}

	if err := tr.WithTx(ctx, func(ctx context.Context) error {
		return s.Insert(ctx, "evt", payload(t, sample{N: 2}))
	}); err != nil {
		t.Fatalf("insert within tx: %v", err)
	}

	if msgs := list(t, tr, s, ctx, 10); len(msgs) != 1 {
		t.Fatalf("list len=%d (ожидалась только запись из tx)", len(msgs))
	}

	// Маркер pgkit.WithTestTx снимает гейт — юнит-тесты потребителя обходятся
	// фейковым транзактором.
	if err := s.Insert(pgkit.WithTestTx(ctx), "evt", payload(t, sample{N: 3})); err != nil {
		t.Fatalf("WithTestTx must satisfy the tx-strict guard: %v", err)
	}
}

func TestStore_PoisonAndBacklog(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	for i := 1; i <= 2; i++ {
		if err := s.Insert(ctx, "evt", payload(t, sample{N: i})); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	msgs := list(t, tr, s, ctx, 10)
	if len(msgs) != 2 {
		t.Fatalf("list len=%d", len(msgs))
	}

	// одно — dead, другое — транзиентный фейл без backoff.
	if err := s.RecordFailures(ctx, []outboxkit.Failure{
		{Ref: msgs[0].Ref(), Err: "boom", Dead: true},
		{Ref: msgs[1].Ref(), Err: "transient"},
	}); err != nil {
		t.Fatalf("record failures: %v", err)
	}

	left := list(t, tr, s, ctx, 10)
	if len(left) != 1 || left[0].ID != msgs[1].ID {
		t.Fatalf("dead не исключён: %+v", left)
	}
	if left[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", left[0].Attempts)
	}

	backlog, err := s.Backlog(ctx)
	if err != nil {
		t.Fatalf("backlog: %v", err)
	}
	if len(backlog) != 1 || backlog[0].Subject != "evt" || backlog[0].Pending != 1 {
		t.Fatalf("backlog = %+v, want 1 pending for evt", backlog)
	}
	if backlog[0].Oldest <= 0 {
		t.Fatalf("backlog oldest = %v, want > 0", backlog[0].Oldest)
	}
}

// Строка с непрошедшим next_attempt_at не вычитывается: без паузы неудачная
// публикация ретраилась бы каждый тик.
func TestStore_BackoffHidesRowUntilDue(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	if err := s.Insert(ctx, "evt", payload(t, sample{N: 1})); err != nil {
		t.Fatalf("insert: %v", err)
	}
	msgs := list(t, tr, s, ctx, 10)

	if err := s.RecordFailures(ctx, []outboxkit.Failure{
		{Ref: msgs[0].Ref(), Err: "nak", RetryAfter: time.Hour},
	}); err != nil {
		t.Fatalf("record failures: %v", err)
	}
	if left := list(t, tr, s, ctx, 10); len(left) != 0 {
		t.Fatalf("row is not due yet, got %d msgs", len(left))
	}

	// Наступление срока возвращает строку в выборку.
	if _, err := db.Exec(ctx, `UPDATE outbox SET next_attempt_at = NOW() - INTERVAL '1 second'`); err != nil {
		t.Fatalf("advance next_attempt_at: %v", err)
	}
	if left := list(t, tr, s, ctx, 10); len(left) != 1 {
		t.Fatalf("due row not returned, got %d msgs", len(left))
	}
}

// Ordered не перескакивает через голову очереди, ждущую backoff: иначе более
// позднее событие ушло бы в стрим раньше неудавшегося.
func TestStore_OrderedWaitsForHeadBackoff(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	for i := 1; i <= 3; i++ {
		if err := s.Insert(ctx, "evt", payload(t, sample{N: i})); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	msgs := listMode(t, tr, s, ctx, 10, outboxkit.PublishOrdered)

	if err := s.RecordFailures(ctx, []outboxkit.Failure{
		{Ref: msgs[0].Ref(), Err: "nak", RetryAfter: time.Hour},
	}); err != nil {
		t.Fatalf("record failures: %v", err)
	}

	if got := listMode(t, tr, s, ctx, 10, outboxkit.PublishOrdered); len(got) != 0 {
		t.Fatalf("ordered jumped the queue head: %d msgs", len(got))
	}
	// batch в той же ситуации дренирует остальные строки.
	if got := list(t, tr, s, ctx, 10); len(got) != 2 {
		t.Fatalf("batch list = %d msgs, want 2", len(got))
	}
}

func TestStore_ReviveDead(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	for i := 1; i <= 3; i++ {
		if err := s.Insert(ctx, "evt", payload(t, sample{N: i})); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if err := s.Insert(ctx, "other", payload(t, sample{N: 4})); err != nil {
		t.Fatalf("insert: %v", err)
	}
	msgs := list(t, tr, s, ctx, 10)
	fails := make([]outboxkit.Failure, len(msgs))
	for i, m := range msgs {
		fails[i] = outboxkit.Failure{Ref: m.Ref(), Err: "boom", Dead: true, RetryAfter: time.Hour}
	}
	if err := s.RecordFailures(ctx, fails); err != nil {
		t.Fatalf("record failures: %v", err)
	}
	if left := list(t, tr, s, ctx, 10); len(left) != 0 {
		t.Fatalf("dead rows still listed: %d", len(left))
	}

	n, err := s.ReviveDead(ctx, outboxkit.ReviveFilter{Subject: "evt"})
	if err != nil {
		t.Fatalf("ReviveDead: %v", err)
	}
	if n != 3 {
		t.Fatalf("revived = %d, want 3 (subject filter)", n)
	}
	revived := list(t, tr, s, ctx, 10)
	if len(revived) != 3 {
		t.Fatalf("listed after revive = %d, want 3", len(revived))
	}
	for _, m := range revived {
		if m.Attempts != 0 {
			t.Fatalf("attempts = %d after revive, want 0", m.Attempts)
		}
	}

	// Фильтр по id и времени: чужой subject остаётся dead.
	n, err = s.ReviveDead(ctx, outboxkit.ReviveFilter{IDs: []int64{msgs[0].ID}, Since: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatalf("ReviveDead by id: %v", err)
	}
	if n != 0 {
		t.Fatalf("revived = %d, want 0 (already alive)", n)
	}
	var dead int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE dead_at IS NOT NULL`).Scan(&dead); err != nil {
		t.Fatalf("count dead: %v", err)
	}
	if dead != 1 {
		t.Fatalf("dead rows = %d, want 1 (other subject untouched)", dead)
	}
}

func TestStore_CustomTable(t *testing.T) {
	db, tr, ctx := startStore(t, testkit.WithMigrations(pgstore.TableMigrations("outbox_orders")))

	s, err := pgstore.NewStoreFor(db, "outbox_orders")
	if err != nil {
		t.Fatalf("NewStoreFor: %v", err)
	}
	if s.Table() != "outbox_orders" {
		t.Fatalf("Table = %q", s.Table())
	}

	if err := s.Insert(ctx, "order.created", payload(t, sample{Foo: "x", N: 1})); err != nil {
		t.Fatalf("insert: %v", err)
	}
	msgs := list(t, tr, s, ctx, 10)
	if len(msgs) != 1 || msgs[0].Subject != "order.created" {
		t.Fatalf("unexpected msgs: %+v", msgs)
	}
	if err := s.MarkPublished(ctx, refs(msgs[0])); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if left := list(t, tr, s, ctx, 10); len(left) != 0 {
		t.Fatalf("want 0 unpublished, got %d", len(left))
	}
}

func TestStore_HappyPath(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)

	if err := s.Insert(ctx, "user.created", payload(t, sample{Foo: "bar", N: 7})); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	msgs := list(t, tr, s, ctx, 100)
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	if msgs[0].Subject != "user.created" || msgs[0].CreatedAt.IsZero() {
		t.Fatalf("unexpected message: %+v", msgs[0])
	}

	if err := s.MarkPublished(ctx, refs(msgs[0])); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	var publishedAt *time.Time
	if err := db.QueryRow(ctx, `SELECT published_at FROM outbox WHERE id = $1`, msgs[0].ID).Scan(&publishedAt); err != nil {
		t.Fatalf("select published_at: %v", err)
	}
	if publishedAt == nil {
		t.Fatal("published_at not set")
	}

	if msgs2 := list(t, tr, s, ctx, 100); len(msgs2) != 0 {
		t.Fatalf("want 0 unpublished after MarkPublished, got %d", len(msgs2))
	}
}

// Предел тела по умолчанию оставляет запас до max_payload NATS: воркер публикует строку в
// JSONB-представлении, а оно длиннее JSON на пробел после каждого `:` и `,` — сильнее всего
// у массива однозначных чисел.
func TestStore_DefaultPayloadLimitFitsNATSMaxPayload(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	digits := make([]int, (outboxkit.DefaultMaxPayloadBytes-1)/2) // JSON `[0,0,…]` у предела
	if err := tr.WithTx(ctx, func(ctx context.Context) error {
		return outboxkit.NewBus("outbox", s).Publish(ctx, "evt", digits)
	}); err != nil {
		t.Fatalf("Publish at the limit: %v", err)
	}
	const natsMaxPayload, headers = 1 << 20, 4 << 10 // дефолт сервера NATS; Nats-Msg-Id и trace
	if n := len(list(t, tr, s, ctx, 1)[0].Payload); n+headers > natsMaxPayload {
		t.Fatalf("published JSONB body %d bytes + headers exceeds NATS max_payload %d", n, natsMaxPayload)
	}
}

// MessageRef несёт ключ партиционирования: UPDATE'ы тика бьют только по своим
// партициям и не задевают соседние.
func TestStore_MarkAndFailAcrossPartitions(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	if err := s.EnsurePartitions(ctx, 1); err != nil {
		t.Fatalf("ensure partitions: %v", err)
	}

	now := time.Now().UTC()
	old := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).AddDate(0, 0, 5)
	oldName, oldSQL := pgstore.PartitionSQL("outbox", old)
	if _, err := db.Exec(ctx, oldSQL); err != nil {
		t.Fatalf("create partition %s: %v", oldName, err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO outbox (subject, payload, created_at) VALUES ('evt', '{"n":0}', $1)`, old); err != nil {
		t.Fatalf("insert into old partition: %v", err)
	}
	if err := s.Insert(ctx, "evt", payload(t, sample{N: 1})); err != nil {
		t.Fatalf("insert: %v", err)
	}

	msgs := list(t, tr, s, ctx, 10)
	if len(msgs) != 2 {
		t.Fatalf("listed %d msgs, want 2", len(msgs))
	}
	if !msgs[0].CreatedAt.Before(msgs[1].CreatedAt) {
		t.Fatalf("unexpected order: %v, %v", msgs[0].CreatedAt, msgs[1].CreatedAt)
	}

	// Отмечаем только строку старой партиции.
	if err := s.MarkPublished(ctx, refs(msgs[0])); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	left := list(t, tr, s, ctx, 10)
	if len(left) != 1 || left[0].ID != msgs[1].ID {
		t.Fatalf("mark hit the wrong partition: %+v", left)
	}

	// И фиксируем неудачу только строке текущей партиции.
	if err := s.RecordFailures(ctx, []outboxkit.Failure{{Ref: msgs[1].Ref(), Err: "nak"}}); err != nil {
		t.Fatalf("RecordFailures: %v", err)
	}
	var attempts int
	if err := db.QueryRow(ctx, `SELECT attempts FROM outbox WHERE id = $1`, msgs[0].ID).Scan(&attempts); err != nil {
		t.Fatalf("select attempts: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("attempts of the other partition row = %d, want 0", attempts)
	}
}

func TestStore_RollbackDropsMessage(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)

	wantErr := errors.New("business failure")
	err := tr.WithTx(ctx, func(ctx context.Context) error {
		if err := s.Insert(ctx, "user.created", payload(t, sample{Foo: "rollback", N: 1})); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithTx err: got %v, want %v", err, wantErr)
	}

	if msgs := list(t, tr, s, ctx, 100); len(msgs) != 0 {
		t.Fatalf("rollback failed — outbox kept message; got %d msgs", len(msgs))
	}

	err = tr.WithTx(ctx, func(ctx context.Context) error {
		return s.Insert(ctx, "user.created", payload(t, sample{Foo: "commit", N: 2}))
	})
	if err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}
	if msgs2 := list(t, tr, s, ctx, 100); len(msgs2) != 1 {
		t.Fatalf("want 1 msg after commit, got %d", len(msgs2))
	}
}

// Ordered: пока одна транзакция держит голову очереди, вторая получает ErrBusy,
// а не следующие строки.
func TestStore_ListOrdered_HeadLockedIsBusy(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	for i := range 4 {
		if err := s.Insert(ctx, "evt", payload(t, sample{N: i})); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	holder := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = tr.WithTx(ctx, func(ctx context.Context) error {
			if _, err := s.ListUnpublished(ctx, 2, outboxkit.PublishOrdered); err != nil {
				return err
			}
			close(holder)
			<-release
			return nil
		})
	}()
	<-holder
	defer close(release)

	err := tr.WithTx(ctx, func(ctx context.Context) error {
		_, err := s.ListUnpublished(ctx, 2, outboxkit.PublishOrdered)
		return err
	})
	if !errors.Is(err, outboxkit.ErrBusy) {
		t.Fatalf("ordered list with locked head: err=%v, want ErrBusy", err)
	}

	// batch-режим в той же ситуации берёт следующие строки.
	var got []outboxkit.Message
	err = tr.WithTx(ctx, func(ctx context.Context) error {
		var err error
		got, err = s.ListUnpublished(ctx, 2, outboxkit.PublishBatch)
		return err
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("batch list: err=%v len=%d, want 2 unlocked rows", err, len(got))
	}
}

func TestStore_EmptyBatchesAreNoops(t *testing.T) {
	s := pgstore.NewStore(nil)
	if err := s.MarkPublished(context.Background(), nil); err != nil {
		t.Fatalf("MarkPublished(nil): %v", err)
	}
	if err := s.RecordFailures(context.Background(), nil); err != nil {
		t.Fatalf("RecordFailures(nil): %v", err)
	}
}

// Ошибки хранилища доходят до вызывающего обёрнутыми, а не теряются.
func TestStore_StorageErrorsSurface(t *testing.T) {
	db, tr, ctx := startStore(t)
	s, err := pgstore.NewStoreFor(db, "outbox_missing")
	if err != nil {
		t.Fatalf("NewStoreFor: %v", err)
	}

	if err := s.Insert(ctx, "evt", payload(t, sample{N: 1})); err == nil {
		t.Fatal("Insert on a missing table must fail")
	}
	if _, err := s.Backlog(ctx); err == nil {
		t.Fatal("Backlog on a missing table must fail")
	}
	if err := s.MarkPublished(ctx, []outboxkit.MessageRef{{ID: 1, CreatedAt: time.Now()}}); err == nil {
		t.Fatal("MarkPublished on a missing table must fail")
	}
	if err := s.RecordFailures(ctx, []outboxkit.Failure{{Ref: outboxkit.MessageRef{ID: 1, CreatedAt: time.Now()}}}); err == nil {
		t.Fatal("RecordFailures on a missing table must fail")
	}
	if _, err := s.ReviveDead(ctx, outboxkit.ReviveFilter{}); err == nil {
		t.Fatal("ReviveDead on a missing table must fail")
	}
	if err := s.EnsurePartitions(ctx, 1); err == nil {
		t.Fatal("EnsurePartitions on a missing table must fail")
	}

	err = tr.WithTx(ctx, func(ctx context.Context) error {
		_, err := s.ListUnpublished(ctx, 10, outboxkit.PublishBatch)
		return err
	})
	if err == nil || errors.Is(err, outboxkit.ErrBusy) {
		t.Fatalf("ListUnpublished err = %v, want a wrapped storage error", err)
	}
}

// Выборка идёт по началу транзакции вставки, а не по порядку коммитов: событие
// из долгой транзакции публикуется раньше того, что закоммитилось первым.
// Домен, которому нужен строгий порядок, сериализует писателей сам.
func TestStore_OrderFollowsTransactionStart(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)

	longStarted := make(chan struct{})
	shortCommitted := make(chan struct{})
	longDone := make(chan error, 1)
	go func() {
		longDone <- tr.WithTx(ctx, func(ctx context.Context) error {
			var started time.Time
			if err := db.QueryRow(ctx, "SELECT transaction_timestamp()").Scan(&started); err != nil {
				return err
			}
			close(longStarted)
			<-shortCommitted
			return s.Insert(ctx, "long", payload(t, sample{N: 1}))
		})
	}()
	<-longStarted

	if err := tr.WithTx(ctx, func(ctx context.Context) error {
		return s.Insert(ctx, "short", payload(t, sample{N: 2}))
	}); err != nil {
		t.Fatalf("short tx: %v", err)
	}
	close(shortCommitted)
	if err := <-longDone; err != nil {
		t.Fatalf("long tx: %v", err)
	}

	msgs := list(t, tr, s, ctx, 10)
	if len(msgs) != 2 {
		t.Fatalf("listed %d msgs, want 2", len(msgs))
	}
	if msgs[0].Subject != "long" || msgs[1].Subject != "short" {
		t.Fatalf("order = %s,%s; ожидался порядок начала транзакций", msgs[0].Subject, msgs[1].Subject)
	}
	// Первой публикуется строка, вставленная и закоммиченная последней.
	if msgs[0].ID < msgs[1].ID {
		t.Fatalf("ids = %d,%d; тест не воспроизвёл пересечение транзакций", msgs[0].ID, msgs[1].ID)
	}
}
