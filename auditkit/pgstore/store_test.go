package pgstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func startStore(t *testing.T) (*pgstore.Store, *testkit.PostgresFixture, context.Context) {
	t.Helper()
	pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return pgstore.NewStore(pg.DB(t)), pg, ctx
}

func TestStore_Log_ErrorPrefix(t *testing.T) {
	s := pgstore.NewStore(nil)
	err := s.Log(context.Background(), auditkit.Entry{
		Action: "x", Resource: "y", Metadata: map[string]any{"bad": make(chan int)},
	})
	if err == nil || !strings.HasPrefix(err.Error(), "auditkit: ") {
		t.Fatalf("err = %v, want auditkit: prefix", err)
	}
}

// Запрос не должен уходить в базу: невалидную Entry ловит Validate.
func TestStore_Log_InvalidEntry(t *testing.T) {
	s := pgstore.NewStore(nil)
	for _, e := range []auditkit.Entry{
		{},
		{Action: "user.create"},
		{Resource: "user"},
		{Action: strings.Repeat("a", auditkit.MaxActionLen+1), Resource: "user"},
	} {
		if err := s.Log(context.Background(), e); !errors.Is(err, auditkit.ErrInvalidEntry) {
			t.Fatalf("Log(%+v) = %v, want ErrInvalidEntry", e, err)
		}
	}
}

func TestStore_Log(t *testing.T) {
	s, pg, ctx := startStore(t)
	db := pg.DB(t)

	at := time.Now().UTC().Truncate(time.Microsecond)
	entry := auditkit.Entry{
		ActorID:    "actor-1",
		Action:     "user.create",
		Resource:   "user",
		ResourceID: "u-42",
		Metadata:   map[string]any{"ip": "127.0.0.1", "n": float64(7)},
		At:         at,
		IP:         "127.0.0.1",
		RequestID:  "req-xyz",
	}
	if err := s.Log(ctx, entry); err != nil {
		t.Fatalf("Log: %v", err)
	}

	var (
		actorID, action, resource, resourceID, ip, reqID string
		metaRaw                                          []byte
		gotAt                                            time.Time
	)
	row := db.QueryRow(ctx, `SELECT actor_id, action, resource, resource_id, metadata::text, ip, request_id, at FROM audit_log WHERE request_id = $1`, "req-xyz")
	if err := row.Scan(&actorID, &action, &resource, &resourceID, &metaRaw, &ip, &reqID, &gotAt); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if actorID != "actor-1" || action != "user.create" || resource != "user" || resourceID != "u-42" || ip != "127.0.0.1" || reqID != "req-xyz" {
		t.Fatalf("unexpected row: %s %s %s %s %s %s", actorID, action, resource, resourceID, ip, reqID)
	}

	var meta map[string]any
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if meta["ip"] != "127.0.0.1" || meta["n"].(float64) != 7 {
		t.Fatalf("metadata content unexpected: %v", meta)
	}

	if !gotAt.Equal(at) {
		t.Fatalf("at mismatch: got %v want %v", gotAt, at)
	}

	// Нулевой At — время базы, NULL-полей нет.
	if err := s.Log(ctx, auditkit.Entry{Action: "session.login", Resource: "session"}); err != nil {
		t.Fatalf("Log zero At: %v", err)
	}
	var dbNow, zeroAt time.Time
	var actor *string
	if err := db.QueryRow(ctx, `SELECT NOW(), at, actor_id FROM audit_log WHERE action = 'session.login'`).Scan(&dbNow, &zeroAt, &actor); err != nil {
		t.Fatalf("scan zero-at row: %v", err)
	}
	if zeroAt.IsZero() || dbNow.Sub(zeroAt) > 30*time.Second || zeroAt.After(dbNow) {
		t.Fatalf("at = %v, want database NOW() (%v)", zeroAt, dbNow)
	}
	if actor != nil {
		t.Fatalf("empty ActorID must be NULL, got %q", *actor)
	}

	var idx int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'audit_log_at_idx'`).Scan(&idx); err != nil {
		t.Fatalf("pg_indexes: %v", err)
	}
	if idx != 1 {
		t.Fatal("audit_log_at_idx missing: миграция 0002 не применилась")
	}
}

// Гарантия кита: внутри Transactor.WithTx запись живёт и откатывается вместе с
// доменной операцией, вне — уходит в autocommit.
func TestStore_Log_JoinsTx(t *testing.T) {
	s, pg, ctx := startStore(t)
	db, tr := pg.DB(t), pg.Transactor(t)

	count := func(action string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
			t.Fatalf("count(%s): %v", action, err)
		}
		return n
	}

	wantErr := errors.New("domain failed")
	err := tr.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.Log(txCtx, auditkit.Entry{Action: "tx.rollback", Resource: "user"}); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithTx = %v, want %v", err, wantErr)
	}
	if n := count("tx.rollback"); n != 0 {
		t.Fatalf("rows after rollback = %d, want 0", n)
	}

	if err := tr.WithTx(ctx, func(txCtx context.Context) error {
		return s.Log(txCtx, auditkit.Entry{Action: "tx.commit", Resource: "user"})
	}); err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}
	if n := count("tx.commit"); n != 1 {
		t.Fatalf("rows after commit = %d, want 1", n)
	}

	if err := s.Log(ctx, auditkit.Entry{Action: "no.tx", Resource: "user"}); err != nil {
		t.Fatalf("Log without tx: %v", err)
	}
	if n := count("no.tx"); n != 1 {
		t.Fatalf("rows written outside tx = %d, want 1", n)
	}
}

func TestStore_Log_InsertError(t *testing.T) {
	s, _, ctx := startStore(t)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err := s.Log(canceled, auditkit.Entry{Action: "user.create", Resource: "user"})
	if err == nil || !strings.HasPrefix(err.Error(), "auditkit: insert: ") {
		t.Fatalf("Log with canceled ctx = %v, want auditkit: insert: prefix", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Log error must wrap context.Canceled, got %v", err)
	}
}

// Миграция 0002 строит audit_log_at_idx с CONCURRENTLY: файл обязан остаться
// однооператорным, иначе драйвер уходит в неявную транзакцию и миграция падает
// на «CREATE INDEX CONCURRENTLY cannot run inside a transaction block».
func TestMigrations_AtIndexIsBuiltConcurrently(t *testing.T) {
	_, pg, ctx := startStore(t)
	db := pg.DB(t)

	var valid bool
	err := db.QueryRow(ctx, `
		SELECT i.indisvalid
		FROM pg_class c
		JOIN pg_index i ON i.indexrelid = c.oid
		WHERE c.relname = 'audit_log_at_idx'`).Scan(&valid)
	if err != nil {
		t.Fatalf("audit_log_at_idx missing after migrations: %v", err)
	}
	if !valid {
		t.Fatal("audit_log_at_idx is invalid: CREATE INDEX CONCURRENTLY did not finish")
	}
}
