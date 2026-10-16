package sessiontest

import (
	"context"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// RacyRetry — фейк, у которого повтор в окне проверяет преемника и гасит его не под одной блокировкой: по одному
// повторы идут как у фейка, а параллельные гасят одного преемника и оставляют сессии каждый свой живой токен.
func RacyRetry(s *Store) session.Store { return racyStore{s} }

type racyStore struct{ *Store }

// racyGap — между проверкой и записью: параллельные повторы успевают проверить одного преемника.
const racyGap = 20 * time.Millisecond

func (s racyStore) Rotate(ctx context.Context, refresh string) (session.Token, error) {
	now := s.now()
	s.mu.Lock()
	tok := s.tokens[authnkit.HashToken(refresh)]
	var successor *fakeToken
	if tok != nil && !tok.usedAt.IsZero() {
		successor = s.tokens[tok.successor]
	}
	retry := successor != nil && s.retry(tok, s.sessions[tok.sessionID], successor, now)
	s.mu.Unlock()
	if !retry || pgkit.InTx(ctx) {
		return s.Store.Rotate(ctx, refresh)
	}
	time.Sleep(racyGap)
	s.mu.Lock()
	defer s.mu.Unlock()
	successor.usedAt = now
	return s.next(tok, s.sessions[tok.sessionID], now), nil
}
