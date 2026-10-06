// Package pgstore — реализация bruteforcekit.Guard и bruteforcekit.Cleaner
// поверх Postgres (pgkit): счётчик попыток в таблице bruteforce_attempts,
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
	db    *pgkit.DB
	cfg   bruteforcekit.Config
	instr *instruments
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
	return &Store{db: db, cfg: cfg, instr: newInstruments()}, nil
}

// Upsert учитывает попытку, только пока активной блокировки нет: WHERE не
// пишет строку под блокировкой, и N одновременных попыток сериализуются на
// строке — пройти успевают не больше MaxFailures. Истёкшая блокировка начинает
// новое окно (failed_count = 1); блокировка ставится попыткой, на которой
// счётчик достигает порога. Всё время — NOW() базы.
const attemptSQL = `
INSERT INTO bruteforce_attempts AS a (subject, ip, failed_count, last_attempt, locked_until)
VALUES ($1, $2, 1, NOW(), CASE WHEN 1 >= $3 THEN NOW() + make_interval(secs => $4) END)
ON CONFLICT (subject, ip) DO UPDATE
SET failed_count = CASE WHEN a.locked_until IS NULL THEN a.failed_count + 1 ELSE 1 END,
    last_attempt = NOW(),
    locked_until = CASE
        WHEN CASE WHEN a.locked_until IS NULL THEN a.failed_count + 1 ELSE 1 END >= $3
        THEN NOW() + make_interval(secs => $4)
    END
WHERE a.locked_until IS NULL OR a.locked_until <= NOW()
RETURNING failed_count, locked_until
`

const activeLockSQL = `
SELECT failed_count, locked_until
FROM bruteforce_attempts
WHERE subject = $1 AND ip = $2 AND locked_until > NOW()
`

// attemptRounds — сколько раз Attempt переучитывает попытку, если блокировка,
// не пустившая upsert, снялась до её чтения (успех параллельного входа,
// истечение).
const attemptRounds = 3

// Attempt учитывает попытку пары (subject, ip) до проверки секрета: nil —
// попытка разрешена, *LockState — пара заблокирована. Внутри транзакции —
// bruteforcekit.ErrInTx, невалидный ключ — bruteforcekit.ErrInvalidKey.
func (s *Store) Attempt(ctx context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	if pgkit.InTx(ctx) {
		return nil, bruteforcekit.ErrInTx
	}
	subject, ip, err := s.cfg.NormalizeKey(subject, ip)
	if err != nil {
		return nil, err
	}
	for range attemptRounds {
		var (
			failedCount int
			lockedUntil *time.Time
		)
		err := s.db.QueryRow(ctx, attemptSQL, subject, ip, s.cfg.MaxFailures, s.cfg.LockDuration.Seconds()).
			Scan(&failedCount, &lockedUntil)
		if err == nil {
			// WHERE пускает upsert только без активной блокировки: locked_until здесь — переход.
			if lockedUntil != nil {
				s.instr.locks.Add(ctx, 1)
			}
			// Счётчик выше порога бывает после понижения MaxFailures: бюджет окна исчерпан.
			if lockedUntil != nil && failedCount > s.cfg.MaxFailures {
				s.instr.checks.Add(ctx, 1, resultAttr(resultLocked))
				return &bruteforcekit.LockState{Until: *lockedUntil, FailedCount: failedCount}, nil
			}
			s.instr.checks.Add(ctx, 1, resultAttr(resultAllowed))
			return nil, nil
		}
		if !pgkit.IsNoRows(err) {
			return nil, fmt.Errorf("bruteforcekit/pgstore: attempt: %w", err)
		}

		var st bruteforcekit.LockState
		err = s.db.QueryRow(ctx, activeLockSQL, subject, ip).Scan(&st.FailedCount, &st.Until)
		if err == nil {
			s.instr.checks.Add(ctx, 1, resultAttr(resultLocked))
			return &st, nil
		}
		if !pgkit.IsNoRows(err) {
			return nil, fmt.Errorf("bruteforcekit/pgstore: attempt: read lock: %w", err)
		}
	}
	return nil, fmt.Errorf("bruteforcekit/pgstore: attempt: lock changed %d times during one attempt", attemptRounds)
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
		return fmt.Errorf("bruteforcekit/pgstore: record success: %w", err)
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
			return fmt.Errorf("bruteforcekit/pgstore: cleanup: %w", err)
		}
		deleted := tag.RowsAffected()
		s.instr.cleanupDeleted.Add(ctx, deleted)
		if deleted < cleanupBatchSize {
			return nil
		}
	}
}
