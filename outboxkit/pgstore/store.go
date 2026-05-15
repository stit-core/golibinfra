package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.stit.tech/stit-infra/golibinfra/outboxkit"
	"git.stit.tech/stit-infra/golibinfra/pgkit"
)

type Store struct {
	db *pgkit.DB
}

func NewStore(db *pgkit.DB) *Store {
	return &Store{db: db}
}

const insertOutboxSQL = `INSERT INTO outbox (subject, payload, created_at) VALUES ($1, $2, NOW())`

func (s *Store) Insert(ctx context.Context, subject string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outboxkit: marshal: %w", err)
	}
	if _, err := s.db.Exec(ctx, insertOutboxSQL, subject, data); err != nil {
		return fmt.Errorf("outboxkit: insert: %w", err)
	}
	return nil
}

const listUnpublishedSQL = `SELECT id, subject, payload FROM outbox WHERE published_at IS NULL ORDER BY created_at, id LIMIT $1 FOR UPDATE SKIP LOCKED`

func (s *Store) ListUnpublished(ctx context.Context, limit int) ([]outboxkit.Message, error) {
	rows, err := s.db.Query(ctx, listUnpublishedSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("outboxkit: list: %w", err)
	}
	defer rows.Close()
	var msgs []outboxkit.Message
	for rows.Next() {
		var m outboxkit.Message
		if err := rows.Scan(&m.ID, &m.Subject, &m.Payload); err != nil {
			return nil, fmt.Errorf("outboxkit: scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outboxkit: rows: %w", err)
	}
	return msgs, nil
}

const markPublishedSQL = `UPDATE outbox SET published_at = $1 WHERE id = $2`

func (s *Store) MarkPublished(ctx context.Context, id int64) error {
	if _, err := s.db.Exec(ctx, markPublishedSQL, time.Now(), id); err != nil {
		return fmt.Errorf("outboxkit: mark published: %w", err)
	}
	return nil
}

const lagBySubjectSQL = `
SELECT subject, EXTRACT(EPOCH FROM (NOW() - MIN(created_at)))::float8 AS lag_seconds
FROM outbox
WHERE published_at IS NULL
GROUP BY subject`

// LagBySubject реализует outboxkit.LagReporter — возвращает возраст
// самого старого неопубликованного события для каждого subject'а.
// Используется outboxkit.RegisterLagGauge для экспонирования
// `outbox_lag_seconds{subject}` как ObservableGauge.
func (s *Store) LagBySubject(ctx context.Context) (map[string]time.Duration, error) {
	rows, err := s.db.Query(ctx, lagBySubjectSQL)
	if err != nil {
		return nil, fmt.Errorf("outboxkit: lag query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]time.Duration)
	for rows.Next() {
		var subj string
		var seconds float64
		if err := rows.Scan(&subj, &seconds); err != nil {
			return nil, fmt.Errorf("outboxkit: lag scan: %w", err)
		}
		out[subj] = time.Duration(seconds * float64(time.Second))
	}
	return out, rows.Err()
}
