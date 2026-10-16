package sessiontest

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// contractConfig — сроки набора: токен живёт час, сессия — три, окно повтора — минута.
var contractConfig = session.Config{IdleTTL: time.Hour, AbsoluteTTL: 3 * time.Hour, CleanupInterval: time.Hour, RetryWindow: time.Minute}

// strictConfig — сроки набора без окна повтора: любой повтор использованного токена отзывает сессию.
var strictConfig = session.Config{IdleTTL: time.Hour, AbsoluteTTL: 3 * time.Hour, CleanupInterval: time.Hour}

var contractStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// RunContract прогоняет контракт session.Store: newStore вызывается на каждый кейс и отдаёт
// хранилище с переданными Config и часами (время двигает набор). Хранилище может
// быть общим для кейсов — subject у каждого кейса свой. Окно повтора у кейсов — минута, у кейсов
// строгого режима (Strict…, ConcurrentRotateHasOneWinner) — ноль.
func RunContract(t *testing.T, newStore func(t *testing.T, cfg session.Config, clock clockkit.Clock) session.Store) {
	t.Helper()
	cases := []struct {
		name   string
		strict bool
		run    func(t *testing.T, s session.Store, clock *clockkit.Tickable)
	}{
		{"IssueAndRotate", false, issueAndRotate},
		{"RetryInWindowIssuesNewSuccessor", false, retryInWindow},
		{"RetryKillsPreviousSuccessor", false, retryKillsPreviousSuccessor},
		{"TwoRetriesInARow", false, twoRetriesInARow},
		{"ReuseOutsideWindowRevokesSession", false, reuseOutsideWindow},
		{"ReuseAfterSuccessorUsedRevokesSession", false, reuseAfterSuccessorUsed},
		{"RetryDoesNotReopenClosedSession", false, retryOfClosedSession},
		{"RetryDoesNotOutliveSession", false, retryAfterAbsoluteDeadline},
		{"ConcurrentRetriesKeepTheSession", false, concurrentRetriesKeepTheSession},
		{"ConcurrentRetriesLeaveOneLiveToken", false, concurrentRetriesLeaveOneLiveToken},
		{"StrictReuseRevokesSession", true, strictReuseRevokesSession},
		{"StrictReuseAfterClockStepBack", true, strictReuseAfterClockStepBack},
		{"ConcurrentRotateHasOneWinner", true, concurrentRotate},
		{"AbsoluteDeadlineIsNotExtended", false, absoluteDeadline},
		{"IdleTokenExpires", false, idleExpiry},
		{"RevokeClosesSession", false, revokeClosesSession},
		{"ConcurrentRevokeHasOneOwner", false, concurrentRevoke},
		{"RevokeAllClosesSubjectSessions", false, revokeAll},
		{"UnknownToken", false, unknownToken},
		{"InvalidSubject", false, invalidSubject},
		{"RotateRefusesTransaction", false, rotateInTx},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := contractConfig
			if c.strict {
				cfg = strictConfig
			}
			clock := clockkit.NewTickable(contractStart)
			c.run(t, newStore(t, cfg, clock), clock)
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

// wantReused — Rotate отозвал сессию tok при повторе: *ReusedError с её сессией и владельцем.
func wantReused(t *testing.T, s session.Store, refresh string, tok session.Token) {
	t.Helper()
	_, err := s.Rotate(t.Context(), refresh)
	var reused *session.ReusedError
	if !errors.As(err, &reused) || !errors.Is(err, session.ErrReused) {
		t.Fatalf("Rotate(used) = %v, want *ReusedError", err)
	}
	if reused.SessionID != tok.SessionID || reused.Subject != tok.Subject {
		t.Fatalf("ReusedError = %+v, want session %s of %s", reused, tok.SessionID, tok.Subject)
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

// Ответ ротации потерян (сбой потребителя после Rotate, обрыв сети): клиент повторяет тем же токеном в окне и
// получает нового преемника той же сессии, сроки — как у ротации в момент повтора; сессия живёт дальше.
func retryInWindow(t *testing.T, s session.Store, clock *clockkit.Tickable) {
	subj := subject(t)
	first := mustIssue(t, s, subj)
	lost := mustRotate(t, s, first.Value)

	clock.Advance(30 * time.Second)
	retried := mustRotate(t, s, first.Value)
	if retried.Value == lost.Value || retried.Value == first.Value || !tokenRe.MatchString(retried.Value) {
		t.Fatalf("retry returned token %q, want a new 64-hex token", retried.Value)
	}
	if retried.SessionID != first.SessionID || retried.Subject != subj {
		t.Fatalf("retry = %+v, want the same session %s of %s", retried, first.SessionID, subj)
	}
	if !retried.ExpiresAt.Equal(clock.Now().Add(time.Hour)) || !retried.SessionExpiresAt.Equal(first.SessionExpiresAt) {
		t.Fatalf("retry deadlines = %s / %s", retried.ExpiresAt, retried.SessionExpiresAt)
	}
	mustRotate(t, s, mustRotate(t, s, retried.Value).Value)
}

// Повтор гасит прежнего преемника: живым остаётся один токен. Его предъявление — повтор (украл ли кто-то ответ или
// преемника получил второй клиент), и сессия отзывается вместе с токеном повтора.
func retryKillsPreviousSuccessor(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	first := mustIssue(t, s, subject(t))
	previous := mustRotate(t, s, first.Value)
	retried := mustRotate(t, s, first.Value)

	wantReused(t, s, previous.Value, first)
	wantRotateErr(t, s, retried.Value, session.ErrInvalid)
}

// Использованный токен вне окна предъявил либо клиент, либо укравший его: сессия отзывается
// целиком, и следующий токен цепочки тоже мёртв; другие сессии владельца живы.
func reuseOutsideWindow(t *testing.T, s session.Store, clock *clockkit.Tickable) {
	subj := subject(t)
	first := mustIssue(t, s, subj)
	next := mustRotate(t, s, first.Value)

	clock.Advance(contractConfig.RetryWindow)
	wantReused(t, s, first.Value, first)
	wantRotateErr(t, s, next.Value, session.ErrInvalid)
	other := mustIssue(t, s, subj)
	mustRotate(t, s, other.Value)
}

// Преемник уже использован — цепочка ушла дальше, и старый токен в окне предъявляет не клиент, чей ответ потерян, а
// второй держатель: сессия отзывается, живой токен цепочки мёртв.
func reuseAfterSuccessorUsed(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	first := mustIssue(t, s, subject(t))
	second := mustRotate(t, s, first.Value)
	live := mustRotate(t, s, second.Value)

	wantReused(t, s, first.Value, first)
	wantRotateErr(t, s, live.Value, session.ErrInvalid)
}

// Окно не открывает закрытую сессию: выход или «выйти везде» после ротации делает повтор её старым токеном отказом.
func retryOfClosedSession(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	first := mustIssue(t, s, subject(t))
	next := mustRotate(t, s, first.Value)
	if _, err := s.Revoke(t.Context(), next.Value); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	wantReused(t, s, first.Value, first)
	wantRotateErr(t, s, next.Value, session.ErrInvalid)
}

// Два ответа подряд потеряны: второй повтор гасит преемника первого, живым остаётся последний выданный, а токен первого
// повтора — погашенный преемник.
func twoRetriesInARow(t *testing.T, s session.Store, clock *clockkit.Tickable) {
	first := mustIssue(t, s, subject(t))
	mustRotate(t, s, first.Value)
	clock.Advance(5 * time.Second)
	firstRetry := mustRotate(t, s, first.Value)
	clock.Advance(5 * time.Second)
	secondRetry := mustRotate(t, s, first.Value)
	if secondRetry.Value == firstRetry.Value || secondRetry.SessionID != first.SessionID {
		t.Fatalf("second retry = %+v, want a new token of session %s", secondRetry, first.SessionID)
	}
	mustRotate(t, s, secondRetry.Value)
	wantReused(t, s, firstRetry.Value, first)
}

// Окно не продлевает сессию: токен повтора у её конца живёт до абсолютного срока, не дальше, а повтор после срока —
// отказ, а не токен, с которым потребитель выдал бы access-токен истёкшей сессии.
func retryAfterAbsoluteDeadline(t *testing.T, s session.Store, clock *clockkit.Tickable) {
	tok := mustIssue(t, s, subject(t))
	for range 3 {
		clock.Advance(50 * time.Minute)
		tok = mustRotate(t, s, tok.Value)
	}
	clock.Advance(29*time.Minute + 30*time.Second)
	mustRotate(t, s, tok.Value)
	clock.Advance(10 * time.Second)
	retried := mustRotate(t, s, tok.Value)
	if deadline := contractStart.Add(contractConfig.AbsoluteTTL); !retried.ExpiresAt.Equal(deadline) || !retried.SessionExpiresAt.Equal(deadline) {
		t.Fatalf("retry at the end of the session expires %s, session %s; want both at the absolute deadline %s",
			retried.ExpiresAt, retried.SessionExpiresAt, deadline)
	}
	clock.Advance(35 * time.Second)
	wantReused(t, s, tok.Value, tok)
}

// parallelRotate — n одновременных Rotate одним токеном: выданные токены в порядке завершения вызовов и ошибки.
func parallelRotate(s session.Store, refresh string, n int) ([]session.Token, []error) {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		issued []session.Token
		errs   []error
	)
	start := make(chan struct{})
	for range n {
		wg.Go(func() {
			<-start
			next, err := s.Rotate(context.Background(), refresh)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			issued = append(issued, next)
		})
	}
	close(start)
	wg.Wait()
	return issued, errs
}

// Параллельные ротации одним токеном в окне — повторы друг друга: проходят по очереди, каждый вызов получает новый
// токен, и сессия жива — честные параллельные обновления клиента её не закрывают; живым остаётся последний выданный.
func concurrentRetriesKeepTheSession(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	subj := subject(t)
	first := mustIssue(t, s, subj)
	const n = 8
	if _, errs := parallelRotate(s, first.Value, n); len(errs) != 0 {
		t.Fatalf("%d parallel Rotate in the window: errors %v, want a token for each", n, errs)
	}
	if owner, err := s.Revoke(t.Context(), first.Value); err != nil || owner != subj {
		t.Fatalf("Revoke after parallel retries = %q, %v; want the open session of %q", owner, err, subj)
	}
}

// Параллельные повторы не раздваивают сессию: из выданных ими токенов живым остаётся один. Иначе вор, повторивший
// украденный токен вместе с клиентом, держит свою ветку сессии, и повтор его не выдаёт. Снаружи живые токены видны
// ротацией: выданные, предъявленные с последнего завершённого вызова, дают не больше одной — первый же погашенный
// отзывает сессию.
func concurrentRetriesLeaveOneLiveToken(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	first := mustIssue(t, s, subject(t))
	const n = 8
	issued, errs := parallelRotate(s, first.Value, n)
	if len(errs) != 0 {
		t.Fatalf("%d parallel Rotate in the window: errors %v, want a token for each", n, errs)
	}
	for _, tok := range issued {
		if tok.SessionID != first.SessionID || tok.Subject != first.Subject {
			t.Fatalf("parallel Rotate issued %+v, want a token of session %s of %s", tok, first.SessionID, first.Subject)
		}
	}
	rotated := 0
	for _, tok := range slices.Backward(issued) {
		_, err := s.Rotate(t.Context(), tok.Value)
		switch {
		case err == nil:
			rotated++
		case !errors.Is(err, session.ErrReused) && !errors.Is(err, session.ErrInvalid):
			t.Fatalf("Rotate of a token issued by a parallel retry = %v, want a token, ErrReused or ErrInvalid", err)
		}
	}
	if rotated > 1 {
		t.Fatalf("%d of %d tokens issued by parallel Rotate in the window rotated, want at most one live", rotated, n)
	}
}

// Без окна часы, шагнувшие назад, повтор не открывают: использованный токен — повтор, сессия отозвана.
func strictReuseAfterClockStepBack(t *testing.T, s session.Store, clock *clockkit.Tickable) {
	first := mustIssue(t, s, subject(t))
	next := mustRotate(t, s, first.Value)
	clock.Set(clock.Now().Add(-time.Second))
	wantReused(t, s, first.Value, first)
	wantRotateErr(t, s, next.Value, session.ErrInvalid)
}

// Без окна повтор использованного токена отзывает сессию сразу, даже если ответ ротации потерян.
func strictReuseRevokesSession(t *testing.T, s session.Store, _ *clockkit.Tickable) {
	first := mustIssue(t, s, subject(t))
	next := mustRotate(t, s, first.Value)

	wantReused(t, s, first.Value, first)
	wantRotateErr(t, s, next.Value, session.ErrInvalid)
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
