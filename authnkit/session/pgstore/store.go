// Package pgstore — session.Store поверх Postgres (pgkit): сессии в authn_sessions,
// хеши refresh-токенов в authn_session_tokens, миграции кита, проверка схемы на
// старте, cleanup-job и fx-проводка; метрики authn_session_*.
package pgstore

import (
	"context"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/idkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// Store — session.Store на таблицах authn_sessions и authn_session_tokens.
type Store struct {
	db    *pgkit.DB
	cfg   session.Config
	clock clockkit.Clock
	instr *instruments
}

var _ session.Store = (*Store)(nil)

// Option — опция NewStore.
type Option func(*Store)

// WithClock подменяет часы сроков; nil оставляет clockkit.Real.
func WithClock(c clockkit.Clock) Option {
	return func(s *Store) {
		if c != nil {
			s.clock = c
		}
	}
}

// NewStore собирает Store и валидирует cfg (session.ErrConfig). Конструктор без fx —
// для CLI; в приложении — Module.
func NewStore(db *pgkit.DB, cfg session.Config, opts ...Option) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Store{db: db, cfg: cfg, clock: clockkit.Real{}, instr: newInstruments()}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Сроки хранятся с точностью Postgres: выданный Token совпадает с записанным.
func (s *Store) now() time.Time {
	return s.clock.Now().UTC().Truncate(time.Microsecond)
}

// Сессия и её первый токен — одним оператором: половины входа не бывает.
const issueSQL = `
WITH s AS (
    INSERT INTO authn_sessions (id, subject, expires_at, created_at)
    VALUES ($1, $2, $3, $4)
    RETURNING id
)
INSERT INTO authn_session_tokens (token_hash, session_id, expires_at, created_at)
SELECT $5, id, $6, $4 FROM s
`

// Issue открывает сессию subject: токен на IdleTTL, сессия на AbsoluteTTL.
func (s *Store) Issue(ctx context.Context, subject string) (session.Token, error) {
	if err := session.CheckSubject(subject); err != nil {
		s.instr.issue(ctx, resultInvalid)
		return session.Token{}, err
	}
	now := s.now()
	t := session.Token{
		Value:            authnkit.NewToken(),
		SessionID:        idkit.NewV7(),
		Subject:          subject,
		ExpiresAt:        now.Add(s.cfg.IdleTTL),
		SessionExpiresAt: now.Add(s.cfg.AbsoluteTTL),
	}
	if _, err := s.db.Exec(ctx, issueSQL, t.SessionID, subject, t.SessionExpiresAt, now,
		authnkit.HashToken(t.Value), t.ExpiresAt); err != nil {
		s.instr.issue(ctx, resultError)
		return session.Token{}, fmt.Errorf("authnkit/session/pgstore: issue: %w", err)
	}
	s.instr.issue(ctx, resultOK)
	return t, nil
}

// Условный UPDATE гасит токен, только если он жив; из параллельных ротаций строку
// получает один, остальные видят used_at. Следующий токен вставляется тем же
// оператором и не переживает абсолютный срок сессии.
const rotateSQL = `
WITH used AS (
    UPDATE authn_session_tokens t
    SET used_at = $3
    FROM authn_sessions s
    WHERE t.token_hash = $1
      AND s.id = t.session_id
      AND t.used_at IS NULL
      AND s.revoked_at IS NULL
      AND t.expires_at > $3
      AND s.expires_at > $3
    RETURNING s.id, s.subject, s.expires_at
)
INSERT INTO authn_session_tokens (token_hash, session_id, expires_at, created_at)
SELECT $2, id, LEAST($4::timestamptz, expires_at), $3 FROM used
RETURNING session_id, expires_at, (SELECT subject FROM used), (SELECT expires_at FROM used)
`

// Rotate гасит токен и выдаёт следующий в той же сессии; внутри транзакции из ctx —
// session.ErrInTx.
func (s *Store) Rotate(ctx context.Context, refresh string) (session.Token, error) {
	if pgkit.InTx(ctx) {
		s.instr.rotate(ctx, resultError)
		return session.Token{}, session.ErrInTx
	}
	now := s.now()
	next := session.Token{Value: authnkit.NewToken()}
	err := s.db.QueryRow(ctx, rotateSQL, authnkit.HashToken(refresh), authnkit.HashToken(next.Value),
		now, now.Add(s.cfg.IdleTTL)).
		Scan(&next.SessionID, &next.ExpiresAt, &next.Subject, &next.SessionExpiresAt)
	if err == nil {
		next.ExpiresAt, next.SessionExpiresAt = next.ExpiresAt.UTC(), next.SessionExpiresAt.UTC()
		s.instr.rotate(ctx, resultOK)
		return next, nil
	}
	if !pgkit.IsNoRows(err) {
		s.instr.rotate(ctx, resultError)
		return session.Token{}, fmt.Errorf("authnkit/session/pgstore: rotate: %w", err)
	}
	result, err := s.reject(ctx, refresh, now)
	s.instr.rotate(ctx, result)
	return session.Token{}, err
}

const lookupSQL = `
SELECT s.id, s.subject, t.used_at IS NOT NULL, s.revoked_at IS NOT NULL, LEAST(t.expires_at, s.expires_at)
FROM authn_session_tokens t
JOIN authn_sessions s ON s.id = t.session_id
WHERE t.token_hash = $1
`

const revokeSessionSQL = `UPDATE authn_sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`

// reject — исход токена, который не прошёл условный UPDATE ротации. Повтор
// использованного токена отзывает сессию: его предъявил либо клиент, либо тот, кто
// украл токен, и различить их нельзя.
func (s *Store) reject(ctx context.Context, refresh string, now time.Time) (string, error) {
	var (
		sessionID, subject string
		used, revoked      bool
		expiresAt          time.Time
	)
	err := s.db.QueryRow(ctx, lookupSQL, authnkit.HashToken(refresh)).Scan(&sessionID, &subject, &used, &revoked, &expiresAt)
	switch {
	case pgkit.IsNoRows(err):
		return resultInvalid, session.ErrInvalid
	case err != nil:
		return resultError, fmt.Errorf("authnkit/session/pgstore: rotate: lookup: %w", err)
	case used:
		tag, err := s.db.Exec(ctx, revokeSessionSQL, sessionID, now)
		if err != nil {
			return resultError, fmt.Errorf("authnkit/session/pgstore: rotate: revoke reused session: %w", err)
		}
		s.instr.revoke(ctx, reasonReused, tag.RowsAffected())
		return resultReused, &session.ReusedError{SessionID: sessionID, Subject: subject}
	case revoked:
		return resultInvalid, session.ErrInvalid
	case !expiresAt.After(now):
		return resultExpired, session.ErrExpired
	}
	// Живой токен не прошёл UPDATE, только если его сессию закрыли между запросами.
	return resultInvalid, session.ErrInvalid
}

const logoutSQL = `
UPDATE authn_sessions SET revoked_at = $2
WHERE revoked_at IS NULL
  AND id = (SELECT session_id FROM authn_session_tokens WHERE token_hash = $1)
`

// Revoke закрывает сессию токена (выход); неизвестный токен или закрытая сессия — nil.
func (s *Store) Revoke(ctx context.Context, refresh string) error {
	tag, err := s.db.Exec(ctx, logoutSQL, authnkit.HashToken(refresh), s.now())
	if err != nil {
		return fmt.Errorf("authnkit/session/pgstore: revoke: %w", err)
	}
	s.instr.revoke(ctx, reasonLogout, tag.RowsAffected())
	return nil
}

const revokeSubjectSQL = `UPDATE authn_sessions SET revoked_at = $2 WHERE subject = $1 AND revoked_at IS NULL`

// RevokeAll закрывает все сессии subject; невалидный subject — session.ErrInvalidSubject.
func (s *Store) RevokeAll(ctx context.Context, subject string) error {
	if err := session.CheckSubject(subject); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, revokeSubjectSQL, subject, s.now())
	if err != nil {
		return fmt.Errorf("authnkit/session/pgstore: revoke all: %w", err)
	}
	s.instr.revoke(ctx, reasonAll, tag.RowsAffected())
	return nil
}

// Батч: за сессией каскадом уходят все её токены, а их у долгой сессии — по одному
// на ротацию.
const cleanupBatchSize = 1000

const cleanupSQL = `
DELETE FROM authn_sessions
WHERE id IN (SELECT id FROM authn_sessions WHERE expires_at < $1 LIMIT $2)
`

// Cleanup удаляет сессии с истёкшим абсолютным сроком вместе с их токенами батчами
// по cleanupBatchSize. Отозванные сессии живут до своего срока: повтор их токенов
// остаётся заметным (session.ErrReused).
func (s *Store) Cleanup(ctx context.Context) error {
	now := s.now()
	for {
		tag, err := s.db.Exec(ctx, cleanupSQL, now, cleanupBatchSize)
		if err != nil {
			return fmt.Errorf("authnkit/session/pgstore: cleanup: %w", err)
		}
		deleted := tag.RowsAffected()
		s.instr.cleanupDeleted.Add(ctx, deleted)
		if deleted < cleanupBatchSize {
			return nil
		}
	}
}
