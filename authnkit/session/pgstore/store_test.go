package pgstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/authnkit/session/pgstore"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func startDB(t *testing.T) (*testkit.PostgresFixture, *pgkit.DB) {
	t.Helper()
	pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	return pg, pg.DB(t)
}

func newStore(t *testing.T, db *pgkit.DB, clock clockkit.Clock) *pgstore.Store {
	t.Helper()
	s, err := pgstore.NewStore(db, contractConfig, pgstore.WithClock(clock))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestStore_Contract(t *testing.T) {
	_, db := startDB(t)
	runContract(t, func(t *testing.T, cfg session.Config, clock clockkit.Clock) session.Store {
		s, err := pgstore.NewStore(db, cfg, pgstore.WithClock(clock))
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		return s
	})
}

func TestNewStore_ValidatesConfig(t *testing.T) {
	t.Parallel()
	if _, err := pgstore.NewStore(nil, session.Config{}); !errors.Is(err, session.ErrConfig) {
		t.Fatalf("NewStore(zero config) = %v, want ErrConfig", err)
	}
}

// Отзыв всех сессий идёт в транзакции смены статуса пользователя: откат смены
// оставляет сессии живыми, коммит — закрывает.
func TestStore_RevokeAllJoinsTransaction(t *testing.T) {
	pg, db := startDB(t)
	tx := pg.Transactor(t)
	s := newStore(t, db, clockkit.Real{})
	ctx := t.Context()
	tok, err := s.Issue(ctx, "tx-user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	errRollback := errors.New("rollback")
	err = tx.WithTx(ctx, func(txCtx context.Context) error {
		if err := s.RevokeAll(txCtx, "tx-user"); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("WithTx = %v, want the rollback error", err)
	}
	if tok, err = s.Rotate(ctx, tok.Value); err != nil {
		t.Fatalf("Rotate after a rolled back RevokeAll: %v", err)
	}

	if err := tx.WithTx(ctx, func(txCtx context.Context) error { return s.RevokeAll(txCtx, "tx-user") }); err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if _, err := s.Rotate(ctx, tok.Value); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("Rotate after a committed RevokeAll = %v, want ErrInvalid", err)
	}
}

// В базе — только хеш: утечка таблицы не даёт действующих токенов.
func TestStore_StoresTokenHash(t *testing.T) {
	_, db := startDB(t)
	s := newStore(t, db, clockkit.Real{})
	ctx := t.Context()
	tok, err := s.Issue(ctx, "hash-user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	var byHash, byValue int
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE token_hash = $1), count(*) FILTER (WHERE token_hash = $2) FROM authn_session_tokens`,
		authnkit.HashToken(tok.Value), tok.Value).Scan(&byHash, &byValue); err != nil {
		t.Fatalf("count: %v", err)
	}
	if byHash != 1 || byValue != 0 {
		t.Fatalf("rows by hash %d, by raw value %d; want 1 and 0", byHash, byValue)
	}
}

func TestStore_CleanupDeletesExpiredSessions(t *testing.T) {
	_, db := startDB(t)
	clock := clockkit.NewTickable(contractStart)
	s := newStore(t, db, clock)
	ctx := t.Context()
	old, err := s.Issue(ctx, "cleanup-user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := s.Rotate(ctx, old.Value); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	clock.Advance(150 * time.Minute)
	fresh, err := s.Issue(ctx, "cleanup-user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clock.Advance(40 * time.Minute) // абсолютный срок первой сессии прошёл, у свежей токен жив

	before := metrictest.Collect(t)
	if err := s.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if got := metrictest.Collect(t).Sub(before).Value("authn_session_cleanup_deleted_total"); got != 1 {
		t.Errorf("authn_session_cleanup_deleted_total delta = %v, want 1", got)
	}
	var sessions, tokens int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM authn_sessions), (SELECT count(*) FROM authn_session_tokens)`).
		Scan(&sessions, &tokens); err != nil {
		t.Fatalf("count: %v", err)
	}
	if sessions != 1 || tokens != 1 {
		t.Fatalf("after Cleanup: %d sessions, %d tokens; want only the fresh session and its token", sessions, tokens)
	}
	if _, err := s.Rotate(ctx, fresh.Value); err != nil {
		t.Fatalf("Rotate of the live session after Cleanup: %v", err)
	}
}

func TestStore_Metrics(t *testing.T) {
	_, db := startDB(t)
	clock := clockkit.NewTickable(contractStart)
	s := newStore(t, db, clock)
	ctx := t.Context()
	before := metrictest.Collect(t)

	if _, err := s.Issue(ctx, ""); !errors.Is(err, session.ErrInvalidSubject) {
		t.Fatalf("Issue(empty) = %v", err)
	}
	a, err := s.Issue(ctx, "metrics-user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	b, err := s.Issue(ctx, "metrics-user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	c, err := s.Issue(ctx, "metrics-user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	next, err := s.Rotate(ctx, a.Value)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	_, _ = s.Rotate(ctx, a.Value)    // reused: сессия a закрыта
	_, _ = s.Rotate(ctx, next.Value) // invalid: сессия закрыта
	_, _ = s.Rotate(ctx, "unknown")  // invalid
	if err := s.Revoke(ctx, b.Value); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	clock.Advance(time.Hour)
	_, _ = s.Rotate(ctx, c.Value) // expired
	if err := s.RevokeAll(ctx, "metrics-user"); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}

	d := metrictest.Collect(t).Sub(before)
	for _, want := range []struct {
		name   string
		labels []string
		value  float64
	}{
		{"authn_session_issued_total", []string{"result", "ok"}, 3},
		{"authn_session_issued_total", []string{"result", "invalid"}, 1},
		{"authn_session_rotations_total", []string{"result", "ok"}, 1},
		{"authn_session_rotations_total", []string{"result", "reused"}, 1},
		{"authn_session_rotations_total", []string{"result", "invalid"}, 2},
		{"authn_session_rotations_total", []string{"result", "expired"}, 1},
		{"authn_session_revoked_total", []string{"reason", "reused"}, 1},
		{"authn_session_revoked_total", []string{"reason", "logout"}, 1},
		{"authn_session_revoked_total", []string{"reason", "all"}, 1},
	} {
		if got := d.Get(t, want.name, want.labels...); got.Kind != metrictest.Counter || got.Value != want.value {
			t.Errorf("%v (%s), want counter delta %v", got, got.Kind, want.value)
		}
	}
}
