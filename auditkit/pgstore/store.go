// Package pgstore — реализация auditkit.Logger поверх Postgres (pgkit):
// один INSERT в таблицу audit_log, миграции кита и fx-проводка.
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// Store — auditkit.Logger на таблице audit_log.
type Store struct {
	db    *pgkit.DB
	instr *instruments
}

var _ auditkit.Logger = (*Store)(nil)

// NewStore собирает Store поверх пула/транзакции pgkit.
func NewStore(db *pgkit.DB) *Store {
	return &Store{db: db, instr: newInstruments()}
}

// Нулевой At → NOW() базы: одни часы для всех записей, как у DEFAULT колонки.
const insertAuditSQL = `
INSERT INTO audit_log (actor_id, action, resource, resource_id, metadata, ip, request_id, at)
VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8::timestamptz, NOW()))
`

// Log пишет запись в audit_log; пустые RequestID и IP — из ctx запроса
// (auditkit.Entry.Enrich). Если ctx получен из pgkit.Transactor.WithTx, INSERT
// участвует в этой транзакции (откат домена откатывает и запись аудита); иначе
// идёт отдельным соединением пула в autocommit. Невалидная Entry —
// auditkit.ErrInvalidEntry, запрос не выполняется.
func (s *Store) Log(ctx context.Context, e auditkit.Entry) error {
	e = e.Enrich(ctx)
	if err := e.Validate(); err != nil {
		s.instr.invalid(ctx)
		return err
	}

	var metaJSON []byte
	if len(e.Metadata) > 0 {
		var err error
		metaJSON, err = json.Marshal(e.Metadata)
		if err != nil {
			s.instr.invalid(ctx)
			return fmt.Errorf("auditkit: marshal metadata: %w", err)
		}
	}

	start := time.Now()
	_, err := s.db.Exec(
		ctx, insertAuditSQL,
		nullStr(e.ActorID),
		e.Action,
		e.Resource,
		nullStr(e.ResourceID),
		nullBytes(metaJSON),
		nullStr(e.IP),
		nullStr(e.RequestID),
		pgkit.Timestamptz(nullTime(e)),
	)
	s.instr.observeWrite(ctx, start, err)
	if err != nil {
		return fmt.Errorf("auditkit: insert: %w", err)
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

func nullTime(e auditkit.Entry) *time.Time {
	if e.At.IsZero() {
		return nil
	}
	return &e.At
}
