package auditkit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.stit.tech/akadmin/golibinfra/pgkit"
)

type pgLogger struct {
	db *pgkit.DB
}

func NewPgLogger(db *pgkit.DB) Logger {
	return &pgLogger{db: db}
}

const insertAuditSQL = `
INSERT INTO audit_log (actor_id, action, resource, resource_id, metadata, ip, request_id, at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
`

func (l *pgLogger) Log(ctx context.Context, e Entry) error {
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

	_, err := l.db.Exec(ctx, insertAuditSQL,
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
