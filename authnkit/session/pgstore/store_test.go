package pgstore_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/authnkit/session/pgstore"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
	"git.stit.tech/stit-core/golibinfra/testkit/sessiontest"
)

// testConfig — сроки тестов хранилища: токен живёт час, сессия — три.
var testConfig = session.Config{IdleTTL: time.Hour, AbsoluteTTL: 3 * time.Hour, CleanupInterval: time.Hour}

var testStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func startDB(t *testing.T) (*testkit.PostgresFixture, *pgkit.DB) {
	t.Helper()
	pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	return pg, pg.DB(t)
}

func newStore(t *testing.T, db *pgkit.DB, clock clockkit.Clock) *pgstore.Store {
	t.Helper()
	s, err := pgstore.NewStore(testConfig, db, pgstore.WithClock(clock))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// Хранилище держит общий контракт session.Store — тот же, что фейк sessiontest.
func TestStore_Contract(t *testing.T) {
	_, db := startDB(t)
	sessiontest.RunContract(t, func(t *testing.T, cfg session.Config, clock clockkit.Clock) session.Store {
		s, err := pgstore.NewStore(cfg, db, pgstore.WithClock(clock))
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		return s
	})
}

func TestNewStore_ValidatesConfig(t *testing.T) {
	t.Parallel()
	if _, err := pgstore.NewStore(session.Config{}, nil); !errors.Is(err, session.ErrConfig) {
		t.Fatalf("NewStore(zero config) = %v, want ErrConfig", err)
	}
}

// Issue, Revoke и RevokeAll идут в транзакции из ctx: откат вызывающего (смены
// статуса пользователя) отменяет вход, выход и отзыв всех сессий, коммит — фиксирует.
func TestStore_WritesJoinTransaction(t *testing.T) {
	pg, db := startDB(t)
	tx := pg.Transactor(t)
	s := newStore(t, db, clockkit.Real{})
	ctx := t.Context()
	errRollback := errors.New("rollback")
	rolledBack := func(write func(context.Context) error) {
		t.Helper()
		err := tx.WithTx(ctx, func(txCtx context.Context) error {
			if err := write(txCtx); err != nil {
				return err
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("WithTx = %v, want the rollback error", err)
		}
	}

	var lost session.Token
	rolledBack(func(txCtx context.Context) error {
		var err error
		lost, err = s.Issue(txCtx, "tx-user")
		return err
	})
	if _, err := s.Rotate(ctx, lost.Value); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("Rotate of a token issued in a rolled back transaction = %v, want ErrInvalid", err)
	}

	tok, err := s.Issue(ctx, "tx-user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	rolledBack(func(txCtx context.Context) error { _, err := s.Revoke(txCtx, tok.Value); return err })
	if tok, err = s.Rotate(ctx, tok.Value); err != nil {
		t.Fatalf("Rotate after a rolled back Revoke: %v", err)
	}
	rolledBack(func(txCtx context.Context) error { return s.RevokeAll(txCtx, "tx-user") })
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

// Очистка удаляет сессии с истёкшим абсолютным сроком; отозванная сессия живёт до
// своего срока, и повтор её токена после очистки — всё ещё ErrReused.
func TestStore_CleanupDeletesExpiredSessions(t *testing.T) {
	_, db := startDB(t)
	clock := clockkit.NewTickable(testStart)
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
	reused, err := s.Issue(ctx, "cleanup-user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := s.Rotate(ctx, reused.Value); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := s.Rotate(ctx, reused.Value); !errors.Is(err, session.ErrReused) {
		t.Fatalf("second Rotate of one token = %v, want ErrReused", err)
	}
	clock.Advance(40 * time.Minute) // абсолютный срок первой сессии прошёл, у свежей токен жив

	// Очистку зовёт job модуля (RTST-02): Run, который ничего не удаляет, тест обязан заметить.
	before := metrictest.Collect(t)
	if err := pgstore.CleanupJob(s, testConfig).Run(ctx); err != nil {
		t.Fatalf("CleanupJob.Run: %v", err)
	}
	if got := metrictest.Collect(t).Sub(before).Value("authn_session_cleanup_deleted_total"); got != 1 {
		t.Errorf("authn_session_cleanup_deleted_total delta = %v, want 1", got)
	}
	var sessions, tokens int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM authn_sessions), (SELECT count(*) FROM authn_session_tokens)`).
		Scan(&sessions, &tokens); err != nil {
		t.Fatalf("count: %v", err)
	}
	if sessions != 2 || tokens != 3 {
		t.Fatalf("after Cleanup: %d sessions, %d tokens; want the fresh and the revoked session with 1 and 2 tokens", sessions, tokens)
	}
	if _, err := s.Rotate(ctx, fresh.Value); err != nil {
		t.Fatalf("Rotate of the live session after Cleanup: %v", err)
	}
	if _, err := s.Rotate(ctx, reused.Value); !errors.Is(err, session.ErrReused) {
		t.Fatalf("reuse of a revoked session's token after Cleanup = %v, want ErrReused", err)
	}
}

func TestStore_Metrics(t *testing.T) {
	_, db := startDB(t)
	clock := clockkit.NewTickable(testStart)
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
	if _, err := s.Revoke(ctx, b.Value); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := s.Revoke(ctx, b.Value); err != nil { // закрытая раньше: сессий в счётчике не прибавляет
		t.Fatalf("Revoke again: %v", err)
	}
	_, _ = s.Rotate(pgkit.WithTestTx(ctx), c.Value) // error: ErrInTx
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
		{"authn_session_rotations_total", []string{"result", "error"}, 1},
		{"authn_session_revoked_total", []string{"reason", "reused"}, 1},
		{"authn_session_revoked_total", []string{"reason", "logout"}, 1},
		{"authn_session_revoked_total", []string{"reason", "all"}, 1},
	} {
		if got := d.Get(t, want.name, want.labels...); got.Kind != metrictest.Counter || got.Value != want.value {
			t.Errorf("%v (%s), want counter delta %v", got, got.Kind, want.value)
		}
	}
}

// cancelAfterLookup — трассировщик pgx: отменяет ctx запроса, как только завершился lookup токена
// (второй запрос Rotate у токена, не прошедшего условный UPDATE).
type cancelAfterLookup struct {
	cancel atomic.Pointer[context.CancelFunc]
	hits   atomic.Int32
}

type lookupKey struct{}

func (c *cancelAfterLookup) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, lookupKey{}, strings.Contains(data.SQL, "LEAST(t.expires_at, s.expires_at)"))
}

func (c *cancelAfterLookup) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if is, _ := ctx.Value(lookupKey{}).(bool); is {
		if cancel := c.cancel.Load(); cancel != nil {
			c.hits.Add(1)
			(*cancel)()
		}
	}
}

// Регрессия RCOR-02: отзыв сессии при повторе токена не зависит от клиента. Запрос жертвы оборван
// между чтением «токен использован» и отзывом (клиент ушёл, дедлайн) — сессия всё равно отозвана,
// и токен того, кто ротировал украденный, больше не работает.
func TestStore_ReuseRevokeSurvivesClientCancel(t *testing.T) {
	pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	tracer := &cancelAfterLookup{}
	pcfg, err := pgxpool.ParseConfig(pg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	pcfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), pcfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := newStore(t, pgkit.NewDB(pool), clockkit.NewTickable(testStart))
	bg := context.WithoutCancel(t.Context())

	stolen, err := s.Issue(bg, "victim")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	thief, err := s.Rotate(bg, stolen.Value)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	tracer.cancel.Store(&cancel)
	_, err = s.Rotate(ctx, stolen.Value)
	tracer.cancel.Store(nil)
	if tracer.hits.Load() == 0 {
		t.Fatal("the lookup of the used token was not traced: the test missed its window")
	}
	if !errors.Is(err, session.ErrReused) {
		t.Fatalf("Rotate of the used token on a cancelled ctx = %v, want ErrReused", err)
	}
	if _, err := s.Rotate(bg, thief.Value); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("the thief's token after the cancelled reuse: %v, want ErrInvalid — the session must be revoked", err)
	}
}
