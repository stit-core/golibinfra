package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

type Store struct {
	db *pgkit.DB
}

func NewStore(db *pgkit.DB) *Store {
	return &Store{db: db}
}

const insertAuditSQL = `
INSERT INTO audit_log (actor_id, action, resource, resource_id, metadata, ip, request_id, at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
`

func (s *Store) Log(ctx context.Context, e auditkit.Entry) error {
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}

	var metaJSON []byte
	if len(e.Metadata) > 0 {
		var err error
		metaJSON, err = json.Marshal(e.Metadata)
		if err != nil {
			return fmt.Errorf("audit: marshal metadata: %w", err)
		}
	}

	_, err := s.db.Exec(
		ctx, insertAuditSQL,
		nullStr(e.ActorID),
		e.Action,
		e.Resource,
		nullStr(e.ResourceID),
		nullBytes(metaJSON),
		nullStr(e.IP),
		nullStr(e.RequestID),
		at,
	)
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
