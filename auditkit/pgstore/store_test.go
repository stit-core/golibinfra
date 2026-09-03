package pgstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestStore_Log_ErrorPrefix(t *testing.T) {
	s := pgstore.NewStore(nil)
	err := s.Log(context.Background(), auditkit.Entry{
		Action: "x", Resource: "y", Metadata: map[string]any{"bad": make(chan int)},
	})
	if err == nil || !strings.HasPrefix(err.Error(), "auditkit: ") {
		t.Fatalf("err = %v, want auditkit: prefix", err)
	}
}

func TestStore_Log(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}

	fx := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	db := fx.DB(t)

	s := pgstore.NewStore(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
