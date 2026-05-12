package bruteforcekit

import (
	"context"
	"time"

	"git.stit.tech/stit/golibinfra/pgkit"
)

type pgGuard struct {
	db  *pgkit.DB
	cfg Config
}

func NewPgGuard(db *pgkit.DB, cfg Config) Guard {
	return &pgGuard{db: db, cfg: cfg}
}

const checkSQL = `
SELECT locked_until
FROM bruteforce_attempts
WHERE subject = $1 AND ip = $2
`

func (g *pgGuard) Check(ctx context.Context, subject, ip string) (*LockState, error) {
	var lockedUntil *time.Time
	err := g.db.QueryRow(ctx, checkSQL, subject, ip).Scan(&lockedUntil)
	if pgkit.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lockedUntil != nil && lockedUntil.After(time.Now()) {
		return &LockState{Until: *lockedUntil}, nil
	}
	return nil, nil
}

const recordFailureSQL = `
INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt)
VALUES ($1, $2, 1, NOW())
ON CONFLICT (subject, ip) DO UPDATE
SET failed_count = bruteforce_attempts.failed_count + 1,
    last_attempt = NOW(),
    locked_until = CASE
        WHEN bruteforce_attempts.failed_count + 1 >= $3
        THEN NOW() + make_interval(secs => $4)
        ELSE NULL
    END
`

func (g *pgGuard) RecordFailure(ctx context.Context, subject, ip string) error {
	lockSecs := int(g.cfg.LockDuration.Seconds())
	_, err := g.db.Exec(ctx, recordFailureSQL, subject, ip, g.cfg.MaxFailures, lockSecs)
	return err
}

const recordSuccessSQL = `
DELETE FROM bruteforce_attempts WHERE subject = $1 AND ip = $2
`

func (g *pgGuard) RecordSuccess(ctx context.Context, subject, ip string) error {
	_, err := g.db.Exec(ctx, recordSuccessSQL, subject, ip)
	return err
}
