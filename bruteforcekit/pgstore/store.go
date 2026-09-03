package pgstore

import (
	"context"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

type Store struct {
	db  *pgkit.DB
	cfg bruteforcekit.Config
}

func NewStore(db *pgkit.DB, cfg bruteforcekit.Config) *Store {
	return &Store{db: db, cfg: cfg}
}

// Сравнение с NOW() базы, а не с часами процесса: locked_until выставляет база.
const checkSQL = `
SELECT locked_until
FROM bruteforce_attempts
WHERE subject = $1 AND ip = $2 AND locked_until > NOW()
`

func (s *Store) Check(ctx context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	var lockedUntil time.Time
	err := s.db.QueryRow(ctx, checkSQL, subject, ip).Scan(&lockedUntil)
	if pgkit.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &bruteforcekit.LockState{Until: lockedUntil}, nil
}

const recordFailureSQL = `
INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt, locked_until)
VALUES ($1, $2, 1, NOW(), CASE WHEN 1 >= $3 THEN NOW() + make_interval(secs => $4) END)
ON CONFLICT (subject, ip) DO UPDATE
SET failed_count = bruteforce_attempts.failed_count + 1,
    last_attempt = NOW(),
    locked_until = CASE
        WHEN bruteforce_attempts.failed_count + 1 >= $3
        THEN NOW() + make_interval(secs => $4)
        ELSE NULL
    END
`

func (s *Store) RecordFailure(ctx context.Context, subject, ip string) error {
	_, err := s.db.Exec(ctx, recordFailureSQL, subject, ip, s.cfg.MaxFailures, s.cfg.LockDuration.Seconds())
	return err
}

const recordSuccessSQL = `
DELETE FROM bruteforce_attempts WHERE subject = $1 AND ip = $2
`

func (s *Store) RecordSuccess(ctx context.Context, subject, ip string) error {
	_, err := s.db.Exec(ctx, recordSuccessSQL, subject, ip)
	return err
}

const cleanupSQL = `
DELETE FROM bruteforce_attempts
WHERE (locked_until IS NULL OR locked_until < NOW())
  AND last_attempt < NOW() - make_interval(secs => $1)
`

func (s *Store) Cleanup(ctx context.Context) error {
	_, err := s.db.Exec(ctx, cleanupSQL, s.cfg.CleanupRetention.Seconds())
	return err
}
