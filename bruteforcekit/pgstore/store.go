// Package pgstore — реализация bruteforcekit.Guard и bruteforcekit.Cleaner
// поверх Postgres (pgkit): счётчик неудач в таблице bruteforce_attempts,
// миграции кита, cleanup-job и fx-проводка.
package pgstore

import (
	"context"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// Store — guard на таблице bruteforce_attempts с ключом (subject, ip).
type Store struct {
	db  *pgkit.DB
	cfg bruteforcekit.Config
}

var (
	_ bruteforcekit.Guard   = (*Store)(nil)
	_ bruteforcekit.Cleaner = (*Store)(nil)
)

// NewStore собирает Store и валидирует cfg: нулевой конфиг выключил бы guard
// молча (locked_until = NOW() + 0s), поэтому это ошибка старта.
func NewStore(db *pgkit.DB, cfg bruteforcekit.Config) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Store{db: db, cfg: cfg}, nil
}

// Сравнение с NOW() базы, а не с часами процесса: locked_until выставляет база.
const checkSQL = `
SELECT failed_count, locked_until
FROM bruteforce_attempts
WHERE subject = $1 AND ip = $2 AND locked_until > NOW()
`

// Check возвращает активную блокировку пары (subject, ip) либо nil. Счётчик не
// трогает; невалидный ключ — bruteforcekit.ErrInvalidKey.
func (s *Store) Check(ctx context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	subject, ip, err := s.cfg.NormalizeKey(subject, ip)
	if err != nil {
		return nil, err
	}

	var (
		failedCount int
		lockedUntil time.Time
	)
	err = s.db.QueryRow(ctx, checkSQL, subject, ip).Scan(&failedCount, &lockedUntil)
	if pgkit.IsNoRows(err) {
		checks.Add(ctx, 1, resultAttr(resultAllowed))
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bruteforcekit: check: %w", err)
	}
	checks.Add(ctx, 1, resultAttr(resultLocked))
	return &bruteforcekit.LockState{Until: lockedUntil, FailedCount: failedCount}, nil
}

// Истёкшая блокировка начинает новое окно (failed_count = 1), а активная
// переживает поднятие MaxFailures — политика описана в README § Инварианты.
// prev — состояние строки до апсерта: по нему считается переход в блокировку.
const recordFailureSQL = `
WITH prev AS (
    SELECT locked_until FROM bruteforce_attempts WHERE subject = $1 AND ip = $2
)
INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt, locked_until)
VALUES ($1, $2, 1, NOW(), CASE WHEN 1 >= $3 THEN NOW() + make_interval(secs => $4) END)
ON CONFLICT (subject, ip) DO UPDATE
SET failed_count = CASE
        WHEN bruteforce_attempts.locked_until <= NOW() THEN 1
        ELSE bruteforce_attempts.failed_count + 1
    END,
    last_attempt = NOW(),
    locked_until = CASE
        WHEN CASE
            WHEN bruteforce_attempts.locked_until <= NOW() THEN 1
            ELSE bruteforce_attempts.failed_count + 1
        END >= $3 THEN NOW() + make_interval(secs => $4)
        WHEN bruteforce_attempts.locked_until > NOW() THEN bruteforce_attempts.locked_until
    END
RETURNING failed_count,
          CASE WHEN locked_until > NOW() THEN locked_until END,
          COALESCE((SELECT p.locked_until > NOW() FROM prev p), false)
`

// RecordFailure учитывает неудачную попытку и возвращает блокировку, если после
// этой попытки она активна; nil — попытки в окне ещё остались.
func (s *Store) RecordFailure(ctx context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	subject, ip, err := s.cfg.NormalizeKey(subject, ip)
	if err != nil {
		return nil, err
	}

	var (
		failedCount int
		lockedUntil *time.Time
		wasLocked   bool
	)
	err = s.db.QueryRow(ctx, recordFailureSQL, subject, ip, s.cfg.MaxFailures, s.cfg.LockDuration.Seconds()).
		Scan(&failedCount, &lockedUntil, &wasLocked)
	if err != nil {
		return nil, fmt.Errorf("bruteforcekit: record failure: %w", err)
	}

	// Считается переход «активной блокировки не было → есть», а не равенство
	// счётчика порогу: понижение MaxFailures между деплоями блокирует строку
	// со счётчиком выше порога, и равенство такой lock не заметило бы.
	if lockedUntil != nil && !wasLocked {
		locks.Add(ctx, 1)
	}
	if lockedUntil == nil {
		return nil, nil
	}
	return &bruteforcekit.LockState{Until: *lockedUntil, FailedCount: failedCount}, nil
}

const recordSuccessSQL = `
DELETE FROM bruteforce_attempts WHERE subject = $1 AND ip = $2
`

// RecordSuccess удаляет строку пары (subject, ip) — успех обнуляет историю.
func (s *Store) RecordSuccess(ctx context.Context, subject, ip string) error {
	subject, ip, err := s.cfg.NormalizeKey(subject, ip)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, recordSuccessSQL, subject, ip); err != nil {
		return fmt.Errorf("bruteforcekit: record success: %w", err)
	}
	return nil
}

// Батч: на разросшейся таблице один безлимитный DELETE не укладывается в
// PG_STATEMENT_TIMEOUT и отменяется, не удалив ничего.
const cleanupBatchSize = 5000

const cleanupSQL = `
DELETE FROM bruteforce_attempts
WHERE ctid IN (
    SELECT ctid FROM bruteforce_attempts
    WHERE (locked_until IS NULL OR locked_until < NOW())
      AND last_attempt < NOW() - make_interval(secs => $1)
    LIMIT $2
)
`

// Cleanup удаляет разблокированные строки старше CleanupRetention батчами по
// cleanupBatchSize; идемпотентен, активные блокировки не трогает.
func (s *Store) Cleanup(ctx context.Context) error {
	for {
		tag, err := s.db.Exec(ctx, cleanupSQL, s.cfg.CleanupRetention.Seconds(), cleanupBatchSize)
		if err != nil {
			return fmt.Errorf("bruteforcekit: cleanup: %w", err)
		}
		deleted := tag.RowsAffected()
		cleanupDeleted.Add(ctx, deleted)
		if deleted < cleanupBatchSize {
			return nil
		}
	}
}
