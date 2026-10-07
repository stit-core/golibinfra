package sessiontest

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// contractConfig — сроки набора: токен живёт час, сессия — три.
var contractConfig = session.Config{IdleTTL: time.Hour, AbsoluteTTL: 3 * time.Hour, CleanupInterval: time.Hour}

var contractStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// RunContract прогоняет контракт session.Store: newStore вызывается на каждый кейс и отдаёт
// хранилище с переданными Config и часами (время двигает набор). Хранилище может
// быть общим для кейсов — subject у каждого кейса свой.
func RunContract(t *testing.T, newStore func(t *testing.T, cfg session.Config, clock clockkit.Clock) session.Store) {
	t.Helper()
	cases := []struct {
		name string
		run  func(t *testing.T, s session.Store, clock *clockkit.Tickable)
	}{
		{"IssueAndRotate", issueAndRotate},
		{"ReuseRevokesSession", reuseRevokesSession},
		{"ConcurrentRotateHasOneWinner", concurrentRotate},
		{"AbsoluteDeadlineIsNotExtended", absoluteDeadline},
		{"IdleTokenExpires", idleExpiry},
		{"RevokeClosesSession", revokeClosesSession},
		{"ConcurrentRevokeHasOneOwner", concurrentRevoke},
		{"RevokeAllClosesSubjectSessions", revokeAll},
		{"UnknownToken", unknownToken},
		{"InvalidSubject", invalidSubject},
		{"RotateRefusesTransaction", rotateInTx},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clock := clockkit.NewTickable(contractStart)
			c.run(t, newStore(t, contractConfig, clock), clock)
		})
	}
}

func subject(t *testing.T) string {
	return "subject-" + strings.ReplaceAll(t.Name(), "/", "-")
}

func mustIssue(t *testing.T, s session.Store, subject string) session.Token {
	t.Helper()
	tok, err := s.Issue(t.Context(), subject)
	if err != nil {
		t.Fatalf("Issue(%s): %v", subject, err)
	}
	return tok
}

func mustRotate(t *testing.T, s session.Store, refresh string) session.Token {
	t.Helper()
	tok, err := s.Rotate(t.Context(), refresh)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	return tok
}

func wantRotateErr(t *testing.T, s session.Store, refresh string, want error) {
	t.Helper()
	if _, err := s.Rotate(t.Context(), refresh); !errors.Is(err, want) {
		t.Fatalf("Rotate = %v, want %v", err, want)
	}
}

var tokenRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func issueAndRotate(t *testing.T, s session.Store, clock *clockkit.Tickable) {
	subj := subject(t)
	first := mustIssue(t, s, subj)
	if !tokenRe.MatchString(first.Value) || first.SessionID == "" || first.Subject != subj {
		t.Fatalf("Issue = %+v, want a 64-hex token of a new session of %s", first, subj)
	}
	if !first.ExpiresAt.Equal(contractStart.Add(time.Hour)) || !first.SessionExpiresAt.Equal(contractStart.Add(3*time.Hour)) {
		t.Fatalf("Issue deadlines = %s / %s, want IdleTTL and AbsoluteTTL from now", first.ExpiresAt, first.SessionExpiresAt)
	}

	clock.Advance(10 * time.Minute)
	next := mustRotate(t, s, first.Value)
	if next.Value == first.Value || !tokenRe.MatchString(next.Value) {
		t.Fatalf("Rotate returned token %q, want a new 64-hex token", next.Value)
	}
	if next.SessionID != first.SessionID || next.Subject != subj {
		t.Fatalf("Rotate = %+v, want the same session %s of %s", next, first.SessionID, subj)
	}
	if !next.ExpiresAt.Equal(clock.Now().Add(time.Hour)) || !next.SessionExpiresAt.Equal(first.SessionExpiresAt) {
		t.Fatalf("Rotate deadlines = %s / %s", next.ExpiresAt, next.SessionExpiresAt)
	}
	mustRotate(t, s, next.Value)
}

// Использованный токен предъявил либо клиент, либо укравший его: сессия отзывается
// целиком, и следующий токен цепочки тоже мёртв.
func reuseRevokesSession(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	subj := subject(t)
	first := mustIssue(t, s, subj)
	next := mustRotate(t, s, first.Value)

	_, err := s.Rotate(t.Context(), first.Value)
	var reused *session.ReusedError
	if !errors.As(err, &reused) || !errors.Is(err, session.ErrReused) {
		t.Fatalf("Rotate(used) = %v, want *ReusedError", err)
	}
	if reused.SessionID != first.SessionID || reused.Subject != subj {
		t.Fatalf("ReusedError = %+v, want session %s of %s", reused, first.SessionID, subj)
	}
	wantRotateErr(t, s, next.Value, session.ErrInvalid)
	other := mustIssue(t, s, subj)
	mustRotate(t, s, other.Value)
}

func concurrentRotate(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	first := mustIssue(t, s, subject(t))
	const n = 8
	var (
		wg              sync.WaitGroup
		mu              sync.Mutex
		won, reused     int
		unexpectedError error
	)
	start := make(chan struct{})
	for range n {
		wg.Go(func() {
			<-start
			_, err := s.Rotate(context.Background(), first.Value)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, session.ErrReused):
				reused++
			default:
				unexpectedError = err
			}
		})
	}
	close(start)
	wg.Wait()
	if won != 1 || reused != n-1 || unexpectedError != nil {
		t.Fatalf("%d parallel Rotate: %d won, %d reused, unexpected %v; want 1 and %d", n, won, reused, unexpectedError, n-1)
	}
}

// Ротация продлевает токен, но не сессию: последний токен упирается в
// абсолютный срок, после него — ErrExpired.
func absoluteDeadline(t *testing.T, s session.Store, clock *clockkit.Tickable) {
	tok := mustIssue(t, s, subject(t))
	for range 3 {
		clock.Advance(50 * time.Minute)
		tok = mustRotate(t, s, tok.Value)
	}
	if !tok.ExpiresAt.Equal(tok.SessionExpiresAt) || !tok.SessionExpiresAt.Equal(contractStart.Add(3*time.Hour)) {
		t.Fatalf("token after rotations expires %s, session %s; want both at the absolute deadline", tok.ExpiresAt, tok.SessionExpiresAt)
	}
	clock.Advance(31 * time.Minute)
	wantRotateErr(t, s, tok.Value, session.ErrExpired)
}

func idleExpiry(t *testing.T, s session.Store, clock *clockkit.Tickable) {
	tok := mustIssue(t, s, subject(t))
	clock.Advance(time.Hour)
	wantRotateErr(t, s, tok.Value, session.ErrExpired)
}

func revokeClosesSession(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	subj := subject(t)
	tok := mustIssue(t, s, subj)
	other := mustIssue(t, s, subj)
	if owner, err := s.Revoke(t.Context(), tok.Value); err != nil || owner != subj {
		t.Fatalf("Revoke = %q, %v; want the owner %q", owner, err, subj)
	}
	// Владельца отдаёт только вызов, закрывший сессию: повтор выхода — не событие аудита.
	if owner, err := s.Revoke(t.Context(), tok.Value); err != nil || owner != "" {
		t.Fatalf("Revoke of a closed session = %q, %v; want empty owner and nil", owner, err)
	}
	if owner, err := s.Revoke(t.Context(), "unknown"); err != nil || owner != "" {
		t.Fatalf("Revoke(unknown) = %q, %v; want empty owner and nil", owner, err)
	}
	wantRotateErr(t, s, tok.Value, session.ErrInvalid)
	mustRotate(t, s, other.Value)
}

// Параллельные выходы одной сессией: владельца получает один вызов — запись аудита одна.
func concurrentRevoke(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	subj := subject(t)
	tok := mustIssue(t, s, subj)
	const n = 8
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		owners []string
		errs   []error
	)
	start := make(chan struct{})
	for range n {
		wg.Go(func() {
			<-start
			owner, err := s.Revoke(context.Background(), tok.Value)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else if owner != "" {
				owners = append(owners, owner)
			}
		})
	}
	close(start)
	wg.Wait()
	if len(errs) != 0 || len(owners) != 1 || owners[0] != subj {
		t.Fatalf("%d parallel Revoke: owners %q, errors %v; want exactly one %q", n, owners, errs, subj)
	}
}

func revokeAll(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	subj := subject(t)
	a, b := mustIssue(t, s, subj), mustIssue(t, s, subj)
	bystander := mustIssue(t, s, subj+"-other")
	if err := s.RevokeAll(t.Context(), subj); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	wantRotateErr(t, s, a.Value, session.ErrInvalid)
	wantRotateErr(t, s, b.Value, session.ErrInvalid)
	mustRotate(t, s, bystander.Value)
	if err := s.RevokeAll(t.Context(), subj); err != nil {
		t.Fatalf("RevokeAll without open sessions: %v", err)
	}
	if owner, err := s.Revoke(t.Context(), a.Value); err != nil || owner != "" {
		t.Fatalf("Revoke after RevokeAll = %q, %v; want empty owner and nil", owner, err)
	}
}

func unknownToken(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	wantRotateErr(t, s, "0000000000000000000000000000000000000000000000000000000000000000", session.ErrInvalid)
	wantRotateErr(t, s, "", session.ErrInvalid)
}

func invalidSubject(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	for _, subj := range []string{"", strings.Repeat("x", session.MaxSubjectLen+1), "a\x00b", "\xff"} {
		if _, err := s.Issue(t.Context(), subj); !errors.Is(err, session.ErrInvalidSubject) {
			t.Errorf("Issue(%q) = %v, want ErrInvalidSubject", subj, err)
		}
	}
	if err := s.RevokeAll(t.Context(), ""); !errors.Is(err, session.ErrInvalidSubject) {
		t.Errorf("RevokeAll(empty) = %v, want ErrInvalidSubject", err)
	}
}

// Отзыв сессии при повторе токена должен пережить откат транзакции вызывающего.
func rotateInTx(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	tok := mustIssue(t, s, subject(t))
	if _, err := s.Rotate(pgkit.WithTestTx(t.Context()), tok.Value); !errors.Is(err, session.ErrInTx) {
		t.Fatalf("Rotate in a transaction = %v, want ErrInTx", err)
	}
	mustRotate(t, s, tok.Value)
}
