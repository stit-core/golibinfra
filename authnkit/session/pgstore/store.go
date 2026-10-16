// Package pgstore — session.Store поверх Postgres (pgkit): сессии в authn_sessions,
// хеши refresh-токенов и их преемников в authn_session_tokens, миграции кита, проверка
// схемы на старте, cleanup-job и fx-проводка; метрики authn_session_*.
package pgstore

import (
	"context"
	"errors"
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
func NewStore(cfg session.Config, db *pgkit.DB, opts ...Option) (*Store, error) {
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

// Условный UPDATE гасит токен, только если он жив, и запоминает его преемника; из параллельных ротаций строку
// получает один, остальные видят used_at. Следующий токен вставляется тем же оператором и не переживает абсолютный
// срок сессии.
const rotateSQL = `
WITH used AS (
    UPDATE authn_session_tokens t
    SET used_at = $3, successor_hash = $2
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

// Повтор в окне: токен использован позже $5 (начало окна), сессия жива, его преемник по снимку оператора не использован
// — тогда строка ответа есть. Преемник гаснет условным UPDATE, и только тогда вставляется новый и родитель указывает на
// него: живым остаётся один токен. Строка без выданного токена — преемника после снимка погасил параллельный повтор.
const retrySQL = `
WITH parent AS (
    SELECT t.successor_hash, s.id, s.subject, s.expires_at
    FROM authn_session_tokens t
    JOIN authn_sessions s ON s.id = t.session_id
    JOIN authn_session_tokens c ON c.token_hash = t.successor_hash
    WHERE t.token_hash = $1
      AND t.used_at > $5
      AND c.used_at IS NULL
      AND s.revoked_at IS NULL
      AND s.expires_at > $3
),
superseded AS (
    UPDATE authn_session_tokens c
    SET used_at = $3
    FROM parent p
    WHERE c.token_hash = p.successor_hash
      AND c.used_at IS NULL
    RETURNING c.session_id
),
repointed AS (
    UPDATE authn_session_tokens t
    SET successor_hash = $2
    FROM superseded
    WHERE t.token_hash = $1
    RETURNING t.session_id
),
issued AS (
    INSERT INTO authn_session_tokens (token_hash, session_id, expires_at, created_at)
    SELECT $2, p.id, LEAST($4::timestamptz, p.expires_at), $3 FROM parent p, repointed
    RETURNING session_id, expires_at
)
SELECT i.session_id, i.expires_at, p.subject, p.expires_at
FROM parent p LEFT JOIN issued i ON true
`

// Rotate гасит токен и выдаёт следующий в той же сессии; повтор использованного токена в окне
// session.Config.RetryWindow гасит его преемника и выдаёт нового; внутри транзакции из ctx — session.ErrInTx.
func (s *Store) Rotate(ctx context.Context, refresh string) (session.Token, error) {
	if pgkit.InTx(ctx) {
		s.instr.rotate(ctx, resultError)
		return session.Token{}, session.ErrInTx
	}
	now := s.now()
	result := resultOK
	next, err := s.rotate(ctx, refresh, now)
	if errors.Is(err, errNotIssued) && s.cfg.RetryWindow > 0 {
		result = resultRetry
		next, err = s.retry(ctx, refresh, now)
	}
	switch {
	case errors.Is(err, errNotIssued):
		result, err = s.reject(ctx, refresh, now)
	case err != nil:
		result = resultError
	}
	s.instr.rotate(ctx, result)
	return next, err
}

// errNotIssued — оператор ротации или повтора не нашёл подходящего токена: исход решает reject.
var errNotIssued = errors.New("authnkit/session/pgstore: no token issued")

// rotate — ротация живого токена: выданный следующий токен; токен не жив — errNotIssued.
func (s *Store) rotate(ctx context.Context, refresh string, now time.Time) (session.Token, error) {
	next := session.Token{Value: authnkit.NewToken()}
	err := s.db.QueryRow(ctx, rotateSQL, authnkit.HashToken(refresh), authnkit.HashToken(next.Value), now, now.Add(s.cfg.IdleTTL)).
		Scan(&next.SessionID, &next.ExpiresAt, &next.Subject, &next.SessionExpiresAt)
	switch {
	case pgkit.IsNoRows(err):
		return session.Token{}, errNotIssued
	case err != nil:
		return session.Token{}, fmt.Errorf("authnkit/session/pgstore: rotate: %w", err)
	}
	next.ExpiresAt, next.SessionExpiresAt = next.ExpiresAt.UTC(), next.SessionExpiresAt.UTC()
	return next, nil
}

// retryRounds — сколько раз повтор идёт заново после гонки: проигрыш — это чужой выигрыш, поэтому из N одновременных
// повторов одного токена каждому хватает N раундов; дальше — отказ, как у прочего повтора.
const retryRounds = 16

// retry — повтор использованного токена в окне: выданный новый преемник. Оператор, проигравший гонку параллельному
// повтору, идёт заново и гасит преемника победителя: параллельные повторы проходят по очереди, как в фейке. Токен не
// годится для повтора — errNotIssued.
func (s *Store) retry(ctx context.Context, refresh string, now time.Time) (session.Token, error) {
	for range retryRounds {
		var (
			next      = session.Token{Value: authnkit.NewToken()}
			sessionID *string
			expiresAt *time.Time
		)
		err := s.db.QueryRow(ctx, retrySQL, authnkit.HashToken(refresh), authnkit.HashToken(next.Value), now,
			now.Add(s.cfg.IdleTTL), now.Add(-s.cfg.RetryWindow)).
			Scan(&sessionID, &expiresAt, &next.Subject, &next.SessionExpiresAt)
		switch {
		case pgkit.IsNoRows(err):
			return session.Token{}, errNotIssued
		case err != nil:
			return session.Token{}, fmt.Errorf("authnkit/session/pgstore: rotate: retry: %w", err)
		case sessionID != nil:
			next.SessionID, next.ExpiresAt, next.SessionExpiresAt = *sessionID, expiresAt.UTC(), next.SessionExpiresAt.UTC()
			return next, nil
		}
	}
	return session.Token{}, errNotIssued
}

// revokeTimeout — срок отзыва сессии при повторе токена: он идёт мимо отмены ctx запроса.
const revokeTimeout = 5 * time.Second

const lookupSQL = `
SELECT s.id, s.subject, t.used_at IS NOT NULL, s.revoked_at IS NOT NULL, LEAST(t.expires_at, s.expires_at)
FROM authn_session_tokens t
JOIN authn_sessions s ON s.id = t.session_id
WHERE t.token_hash = $1
`

const revokeSessionSQL = `UPDATE authn_sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`

// reject — исход токена, который не прошёл ни ротацию, ни повтор в окне. Повтор использованного токена отзывает
// сессию: вне окна, после использования преемника или погашенный повтором преемник его предъявил либо клиент, либо
// тот, кто украл токен, и различить их нельзя.
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
		// Отзыв не зависит от клиента: запрос, оборванный между чтением «токен использован» и
		// отзывом, оставил бы сессию живой — и токен того, кто ротировал украденный, рабочим.
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
		defer cancel()
		tag, err := s.db.Exec(revokeCtx, revokeSessionSQL, sessionID, now)
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

// Условие revoked_at IS NULL: из параллельных выходов одной сессии владельца получает один.
const logoutSQL = `
UPDATE authn_sessions SET revoked_at = $2
WHERE revoked_at IS NULL
  AND id = (SELECT session_id FROM authn_session_tokens WHERE token_hash = $1)
RETURNING subject
`

// Revoke закрывает сессию токена (выход) и отдаёт её владельца; неизвестный токен или
// закрытая раньше сессия — "" и nil.
func (s *Store) Revoke(ctx context.Context, refresh string) (string, error) {
	var subject string
	err := s.db.QueryRow(ctx, logoutSQL, authnkit.HashToken(refresh), s.now()).Scan(&subject)
	switch {
	case pgkit.IsNoRows(err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("authnkit/session/pgstore: revoke: %w", err)
	}
	s.instr.revoke(ctx, reasonLogout, 1)
	return subject, nil
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
