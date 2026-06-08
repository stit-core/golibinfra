package pgstore_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func extractMigrations(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := fs.WalkDir(pgstore.MigrationsFS, "migrations", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := pgstore.MigrationsFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(path)), data, 0o644)
	})
	if err != nil {
		t.Fatalf("extract migrations: %v", err)
	}
	return dir
}

func TestStore_Log(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}

	migDir := extractMigrations(t)
	fx := testkit.StartPostgres(t, migDir)
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
}
