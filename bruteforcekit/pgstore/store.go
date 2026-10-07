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

// Store — guard на таблице bruteforce_attempts с ключом (subject, ip); бюджет субъекта — строка
// subject'а с пустым ip.
type Store struct {
	db    *pgkit.DB
	cfg   bruteforcekit.Config
	instr *instruments
	hooks *testHooks
}

// testHooks — точки тестов пакета; указатель, а не поле-функция, держит Store сравнимым.
type testHooks struct {
	// lockSeen — между upsert'ом, увидевшим блокировку пары, и её чтением.
	lockSeen func()
	// subjectReset — в RecordSuccess между обнулением строки субъекта и удалением строки пары.
	subjectReset func()
}

var (
	_ bruteforcekit.Guard   = (*Store)(nil)
	_ bruteforcekit.Cleaner = (*Store)(nil)
)

// NewStore собирает Store и валидирует cfg: нулевой конфиг выключил бы guard
// молча (locked_until = NOW() + 0s), поэтому это ошибка старта.
func NewStore(cfg bruteforcekit.Config, db *pgkit.DB) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Store{db: db, cfg: cfg, instr: newInstruments()}, nil
}

// windowUpsert — учёт попытки в строке счётчика, только пока активной блокировки нет: WHERE не
// пишет строку под блокировкой, и одновременные попытки сериализуются на строке. Истёкшая
// блокировка начинает новое окно (failed_count = 1); блокировку ставит попытка, на которой
// счётчик достигает порога. %[1]s — ip строки, %[2]s и %[3]s — порог и срок, %[4]s — условие.
const windowUpsert = `
    INSERT INTO bruteforce_attempts AS a (subject, ip, failed_count, last_attempt, locked_until)
    SELECT $1, %[1]s, 1, NOW(), CASE WHEN 1 >= %[2]s THEN NOW() + make_interval(secs => %[3]s) END
    WHERE %[4]s
    ON CONFLICT (subject, ip) DO UPDATE
    SET failed_count = CASE WHEN a.locked_until IS NULL THEN a.failed_count + 1 ELSE 1 END,
        last_attempt = NOW(),
        locked_until = CASE
            WHEN CASE WHEN a.locked_until IS NULL THEN a.failed_count + 1 ELSE 1 END >= %[2]s
            THEN NOW() + make_interval(secs => %[3]s)
        END
    WHERE a.locked_until IS NULL OR a.locked_until <= NOW()
    RETURNING failed_count, locked_until`

// attemptSQL — попытка одним оператором. Порядок блокировок у всех операторов кита один: строка
// субъекта ($5 — бюджет включён), затем строка пары. Поэтому строка субъекта берётся FOR UPDATE
// первой и обязана уже быть: без неё оператор ничего не пишет и отдаёт subject_missing —
// Attempt создаёт её (createSubjectSQL) и повторяет. Попытки одного subject со всех адресов идут
// по очереди, бюджет не превышается; под блокировкой субъекта пара не учитывается, под
// блокировкой пары — субъект. Всё время — NOW() базы.
var attemptSQL = `
WITH subject_row AS (
    SELECT failed_count, locked_until FROM bruteforce_attempts
    WHERE $5 AND subject = $1 AND ip = '' FOR UPDATE
), subject_lock AS (
    SELECT failed_count, locked_until FROM subject_row WHERE locked_until > NOW()
), ready AS (
    SELECT 1 WHERE NOT $5 OR EXISTS (SELECT 1 FROM subject_row)
), pair AS (` + fmt.Sprintf(windowUpsert, "$2", "$3", "$4", "EXISTS (SELECT 1 FROM ready) AND NOT EXISTS (SELECT 1 FROM subject_lock)") + `
), subject AS (` + fmt.Sprintf(windowUpsert, "''", "$6", "$7", "$5 AND EXISTS (SELECT 1 FROM pair)") + `
)
SELECT NOT EXISTS (SELECT 1 FROM ready), (SELECT failed_count FROM subject_lock), (SELECT locked_until FROM subject_lock),
       EXISTS (SELECT 1 FROM pair), (SELECT failed_count FROM pair), (SELECT locked_until FROM pair),
       (SELECT failed_count FROM subject), (SELECT locked_until FROM subject),
       NOW()
`

// createSubjectSQL — пустая строка субъекта до попытки: одна строка, своих блокировок оператор не
// держит и цикла ожиданий не замкнёт.
const createSubjectSQL = `
INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt) VALUES ($1, '', 0, NOW())
ON CONFLICT (subject, ip) DO NOTHING
`

const activeLockSQL = `
SELECT failed_count, locked_until, NOW()
FROM bruteforce_attempts
WHERE subject = $1 AND ip = $2 AND locked_until > NOW()
`

// attemptRounds — сколько раз Attempt повторяет оператор: строку субъекта удалил cleanup до
// оператора или блокировка, не пустившая upsert, снялась до её чтения (успех параллельного входа,
// истечение).
const attemptRounds = 4

// attemptResult — строка attemptSQL: нет строки субъекта, блокировка субъекта до попытки, учёт
// пары и субъекта.
type attemptResult struct {
	subjectMissing   bool
	subjectLockCount *int
	subjectLockUntil *time.Time
	pairCounted      bool
	pairCount        *int
	pairUntil        *time.Time
	subjectCount     *int
	subjectUntil     *time.Time
	now              time.Time
}

// Attempt учитывает попытку пары (subject, ip) и subject'а до проверки секрета: nil —
// попытка разрешена, *LockState — пара или subject заблокированы. Внутри транзакции —
// bruteforcekit.ErrInTx, невалидный ключ — bruteforcekit.ErrInvalidKey.
func (s *Store) Attempt(ctx context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	if pgkit.InTx(ctx) {
		return nil, bruteforcekit.ErrInTx
	}
	subject, ip, err := s.cfg.NormalizeKey(subject, ip)
	if err != nil {
		return nil, err
	}
	budget := s.cfg.SubjectMaxFailures != bruteforcekit.SubjectBudgetOff
	for range attemptRounds {
		var r attemptResult
		err := s.db.QueryRow(ctx, attemptSQL, subject, ip, s.cfg.MaxFailures, s.cfg.LockDuration.Seconds(),
			budget, s.cfg.SubjectMaxFailures, s.cfg.SubjectLockDuration.Seconds()).
			Scan(&r.subjectMissing, &r.subjectLockCount, &r.subjectLockUntil, &r.pairCounted, &r.pairCount, &r.pairUntil,
				&r.subjectCount, &r.subjectUntil, &r.now)
		if err != nil {
			return nil, fmt.Errorf("bruteforcekit/pgstore: attempt: %w", err)
		}
		if r.subjectMissing {
			if _, err := s.db.Exec(ctx, createSubjectSQL, subject); err != nil {
				return nil, fmt.Errorf("bruteforcekit/pgstore: attempt: create subject row: %w", err)
			}
			continue
		}
		if r.subjectLockUntil != nil {
			return s.locked(ctx, bruteforcekit.ScopeSubject, *r.subjectLockCount, *r.subjectLockUntil, r.now), nil
		}
		if r.pairCounted && r.pairUntil != nil {
			s.instr.locks.Add(ctx, 1, scopeAttr(bruteforcekit.ScopePair))
		}
		if r.subjectUntil != nil {
			s.instr.locks.Add(ctx, 1, scopeAttr(bruteforcekit.ScopeSubject))
		}
		// Счётчик выше порога бывает после понижения порога: бюджет окна исчерпан. Строка субъекта
		// под нашей блокировкой и без своей: учтённая пара — учтённый субъект.
		switch {
		case r.pairCounted && r.pairUntil != nil && *r.pairCount > s.cfg.MaxFailures:
			return s.locked(ctx, bruteforcekit.ScopePair, *r.pairCount, *r.pairUntil, r.now), nil
		case r.subjectUntil != nil && *r.subjectCount > s.cfg.SubjectMaxFailures:
			return s.locked(ctx, bruteforcekit.ScopeSubject, *r.subjectCount, *r.subjectUntil, r.now), nil
		case r.pairCounted:
			s.instr.checks.Add(ctx, 1, resultAttr(resultAllowed))
			return nil, nil
		}

		// Пару не пустила её блокировка: срок и счётчик — чтением строки, upsert их не отдаёт.
		if s.hooks != nil && s.hooks.lockSeen != nil {
			s.hooks.lockSeen()
		}
		var (
			count      int
			until, now time.Time
		)
		err = s.db.QueryRow(ctx, activeLockSQL, subject, ip).Scan(&count, &until, &now)
		if err == nil {
			return s.locked(ctx, bruteforcekit.ScopePair, count, until, now), nil
		}
		if !pgkit.IsNoRows(err) {
			return nil, fmt.Errorf("bruteforcekit/pgstore: attempt: read lock: %w", err)
		}
	}
	return nil, fmt.Errorf("bruteforcekit/pgstore: attempt: rows changed %d times during one attempt", attemptRounds)
}

// locked — ответ «заблокировано» с остатком по часам базы.
func (s *Store) locked(ctx context.Context, scope bruteforcekit.LockScope, count int, until, now time.Time) *bruteforcekit.LockState {
	s.instr.checks.Add(ctx, 1, resultAttr(resultLocked))
	return &bruteforcekit.LockState{Until: until, RetryAfter: max(until.Sub(now), 0), FailedCount: count, Scope: scope}
}

// resetSubjectSQL обнуляет окно субъекта, а не удаляет строку: следующей попытке её не создавать,
// и параллельный Attempt не уходит на повтор; старую строку уберёт cleanup.
const resetSubjectSQL = `
UPDATE bruteforce_attempts SET failed_count = 0, locked_until = NULL, last_attempt = NOW()
WHERE subject = $1 AND ip = ''
`

const deletePairSQL = `DELETE FROM bruteforce_attempts WHERE subject = $1 AND ip = $2`

// RecordSuccess обнуляет окно субъекта и удаляет строку пары (subject, ip) — успех обнуляет
// историю. Двумя операторами в порядке блокировок Attempt (субъект, затем пара): в транзакции
// вызывающего порядок общий, вне её оператор держит одну строку.
func (s *Store) RecordSuccess(ctx context.Context, subject, ip string) error {
	subject, ip, err := s.cfg.NormalizeKey(subject, ip)
	if err != nil {
		return err
	}
	if s.cfg.SubjectMaxFailures != bruteforcekit.SubjectBudgetOff {
		if _, err := s.db.Exec(ctx, resetSubjectSQL, subject); err != nil {
			return fmt.Errorf("bruteforcekit/pgstore: record success: %w", err)
		}
	}
	if s.hooks != nil && s.hooks.subjectReset != nil {
		s.hooks.subjectReset()
	}
	if _, err := s.db.Exec(ctx, deletePairSQL, subject, ip); err != nil {
		return fmt.Errorf("bruteforcekit/pgstore: record success: %w", err)
	}
	return nil
}

// Батч: на разросшейся таблице один безлимитный DELETE не укладывается в
// PG_STATEMENT_TIMEOUT и отменяется, не удалив ничего.
const cleanupBatchSize = 5000

// SKIP LOCKED: cleanup не ждёт строк, занятых попыткой, и не встаёт в цикл ожиданий с ней (порядок
// ctid — не порядок блокировок Attempt); занятая строка не старая, её уберёт следующий прогон.
const cleanupSQL = `
DELETE FROM bruteforce_attempts
WHERE ctid IN (
    SELECT ctid FROM bruteforce_attempts
    WHERE (locked_until IS NULL OR locked_until < NOW())
      AND last_attempt < NOW() - make_interval(secs => $1)
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
`

// Cleanup удаляет разблокированные строки старше CleanupRetention батчами по
// cleanupBatchSize; идемпотентен, активные блокировки и строки, занятые попыткой, не трогает.
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
