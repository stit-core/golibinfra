// Package sessiontest — фейк session.Store для юнит-тестов без Postgres и общий
// контрактный набор Run: фейк и authnkit/session/pgstore проверяются одними кейсами.
package sessiontest

import (
	"context"
	"sync"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/idkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// Store — in-memory session.Store с семантикой pgstore: те же сроки, ротация,
// отзыв сессии при повторе токена и ErrInTx у Rotate в транзакции. Откат
// транзакции фейк не видит: отзыв в транзакции проверяется на pgstore.
type Store struct {
	cfg   session.Config
	clock clockkit.Clock

	mu       sync.Mutex
	sessions map[string]*fakeSession
	tokens   map[string]*fakeToken // ключ — authnkit.HashToken
}

type fakeSession struct {
	subject   string
	expiresAt time.Time
	revoked   bool
}

type fakeToken struct {
	sessionID string
	expiresAt time.Time
	used      bool
}

var _ session.Store = (*Store)(nil)

// New собирает фейк; невалидный cfg — session.ErrConfig, nil-часы — clockkit.Real.
func New(cfg session.Config, clock clockkit.Clock) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = clockkit.Real{}
	}
	return &Store{cfg: cfg, clock: clock, sessions: map[string]*fakeSession{}, tokens: map[string]*fakeToken{}}, nil
}

// Сроки — с точностью Postgres, как у pgstore.
func (s *Store) now() time.Time {
	return s.clock.Now().UTC().Truncate(time.Microsecond)
}

// Issue открывает сессию subject; невалидный subject — session.ErrInvalidSubject.
func (s *Store) Issue(_ context.Context, subject string) (session.Token, error) {
	if err := session.CheckSubject(subject); err != nil {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[t.SessionID] = &fakeSession{subject: subject, expiresAt: t.SessionExpiresAt}
	s.tokens[authnkit.HashToken(t.Value)] = &fakeToken{sessionID: t.SessionID, expiresAt: t.ExpiresAt}
	return t, nil
}

// Rotate гасит токен и выдаёт следующий; исходы — как у session.Store.Rotate.
func (s *Store) Rotate(ctx context.Context, refresh string) (session.Token, error) {
	if pgkit.InTx(ctx) {
		return session.Token{}, session.ErrInTx
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.tokens[authnkit.HashToken(refresh)]
	if !ok {
		return session.Token{}, session.ErrInvalid
	}
	sess := s.sessions[tok.sessionID]
	switch {
	case tok.used:
		sess.revoked = true
		return session.Token{}, &session.ReusedError{SessionID: tok.sessionID, Subject: sess.subject}
	case sess.revoked:
		return session.Token{}, session.ErrInvalid
	case !tok.expiresAt.After(now) || !sess.expiresAt.After(now):
		return session.Token{}, session.ErrExpired
	}
	tok.used = true
	expiresAt := now.Add(s.cfg.IdleTTL)
	if sess.expiresAt.Before(expiresAt) {
		expiresAt = sess.expiresAt
	}
	next := session.Token{
		Value:            authnkit.NewToken(),
		SessionID:        tok.sessionID,
		Subject:          sess.subject,
		ExpiresAt:        expiresAt,
		SessionExpiresAt: sess.expiresAt,
	}
	s.tokens[authnkit.HashToken(next.Value)] = &fakeToken{sessionID: next.SessionID, expiresAt: next.ExpiresAt}
	return next, nil
}

// Revoke закрывает сессию токена и отдаёт её владельца; неизвестный токен или закрытая
// раньше сессия — "" и nil.
func (s *Store) Revoke(_ context.Context, refresh string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.tokens[authnkit.HashToken(refresh)]
	if !ok {
		return "", nil
	}
	sess := s.sessions[tok.sessionID]
	if sess.revoked {
		return "", nil
	}
	sess.revoked = true
	return sess.subject, nil
}

// RevokeAll закрывает все сессии subject; невалидный subject — session.ErrInvalidSubject.
func (s *Store) RevokeAll(_ context.Context, subject string) error {
	if err := session.CheckSubject(subject); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.subject == subject {
			sess.revoked = true
		}
	}
	return nil
}

// Active — число открытых сессий subject: не отозванных и не истёкших по
// абсолютному сроку. Для проверок «блокировка закрыла сессии» в тестах сценариев.
func (s *Store) Active(subject string) int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sess := range s.sessions {
		if sess.subject == subject && !sess.revoked && sess.expiresAt.After(now) {
			n++
		}
	}
	return n
}
