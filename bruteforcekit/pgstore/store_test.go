package pgstore_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/bruteforcekit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func config() bruteforcekit.Config {
	return bruteforcekit.Config{
		MaxFailures:      3,
		LockDuration:     15 * time.Minute,
		CleanupRetention: 24 * time.Hour,
		CleanupInterval:  5 * time.Minute,
		IPv6Prefix:       bruteforcekit.FullIPv6Prefix,
	}
}

func startDB(t *testing.T) (*pgkit.DB, context.Context) {
	t.Helper()
	pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return pg.DB(t), ctx
}

func newStore(t *testing.T, db *pgkit.DB, cfg bruteforcekit.Config) *pgstore.Store {
	t.Helper()
	s, err := pgstore.NewStore(db, cfg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestNewStore_ValidatesConfig(t *testing.T) {
	t.Parallel()
	// Нулевой конфиг выключил бы guard молча: locked_until = NOW() + 0s.
	if _, err := pgstore.NewStore(nil, bruteforcekit.Config{}); !errors.Is(err, bruteforcekit.ErrConfig) {
		t.Fatalf("NewStore(zero config) = %v, want ErrConfig", err)
	}
	cfg := config()
	cfg.LockDuration = 0
	if _, err := pgstore.NewStore(nil, cfg); !errors.Is(err, bruteforcekit.ErrConfig) {
		t.Fatalf("NewStore(LockDuration=0) = %v, want ErrConfig", err)
	}
}

func TestStore_InvalidKey(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil, config())
	ctx := t.Context()

	// Запрос не уходит в базу (db == nil): ключ проверяется до него.
	if _, err := s.Attempt(ctx, "", "203.0.113.7"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("Attempt(empty subject) = %v, want ErrInvalidKey", err)
	}
	if _, err := s.Attempt(ctx, "u", "not-an-ip"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("Attempt(bad ip) = %v, want ErrInvalidKey", err)
	}
	if err := s.RecordSuccess(ctx, strings.Repeat("s", bruteforcekit.MaxSubjectLen+1), "203.0.113.7"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("RecordSuccess(long subject) = %v, want ErrInvalidKey", err)
	}
	if _, err := s.Attempt(pgkit.WithTestTx(ctx), "u", "203.0.113.7"); !errors.Is(err, bruteforcekit.ErrInTx) {
		t.Fatalf("Attempt(in tx) = %v, want ErrInTx", err)
	}
}

// Регрессия: логин в Transactor.WithTx откатывает транзакцию на неверном
// пароле — учтённая в ней попытка исчезла бы, и перебор шёл бы без предела.
// Attempt внутри транзакции — ErrInTx и строки нет; вне неё счёт идёт как обычно.
func TestStore_AttemptOutsideTransaction(t *testing.T) {
	pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, tr := pg.DB(t), pg.Transactor(t)
	cfg := config()
	s := newStore(t, db, cfg)
	const subject, ip = "victim@example.com", "203.0.113.7"
	errBadPassword := errors.New("invalid credentials")

	for range 2 * cfg.MaxFailures {
		err := tr.WithTx(ctx, func(ctx context.Context) error {
			if _, err := s.Attempt(ctx, subject, ip); err != nil {
				return err
			}
			return errBadPassword
		})
		if !errors.Is(err, bruteforcekit.ErrInTx) {
			t.Fatalf("Attempt inside WithTx = %v, want ErrInTx", err)
		}
	}
	if n := failedCount(t, ctx, db, subject, ip); n != -1 {
		t.Fatalf("row written by a rejected in-tx attempt: failed_count = %d", n)
	}

	for range cfg.MaxFailures {
		if st, err := s.Attempt(ctx, subject, ip); err != nil || st != nil {
			t.Fatalf("Attempt outside tx = %v, %v, want allowed", st, err)
		}
		if err := tr.WithTx(ctx, func(context.Context) error { return errBadPassword }); !errors.Is(err, errBadPassword) {
			t.Fatalf("WithTx: %v", err)
		}
	}
	if st, err := s.Attempt(ctx, subject, ip); err != nil || st == nil {
		t.Fatalf("Attempt after %d failures = %v, %v, want lock", cfg.MaxFailures, st, err)
	}

	// Откат успеха оставляет счётчик: fail-closed.
	if err := tr.WithTx(ctx, func(ctx context.Context) error {
		if err := s.RecordSuccess(ctx, subject, ip); err != nil {
			return err
		}
		return errBadPassword
	}); !errors.Is(err, errBadPassword) {
		t.Fatalf("RecordSuccess inside WithTx: %v", err)
	}
	if n := failedCount(t, ctx, db, subject, ip); n != cfg.MaxFailures {
		t.Fatalf("failed_count after rolled-back success = %d, want %d", n, cfg.MaxFailures)
	}
}

// attempts делает n попыток пары и требует, чтобы все прошли.
func attempts(t *testing.T, ctx context.Context, s *pgstore.Store, subject, ip string, n int) {
	t.Helper()
	for i := range n {
		if st, err := s.Attempt(ctx, subject, ip); err != nil || st != nil {
			t.Fatalf("Attempt %d of %q/%q = %v, %v, want allowed", i+1, subject, ip, st, err)
		}
	}
}

func TestStore_Guard(t *testing.T) {
	db, ctx := startDB(t)

	t.Run("locks after max attempts", func(t *testing.T) {
		cfg := config()
		s := newStore(t, db, cfg)
		const subject, ip = "user@example.com", "10.0.0.1"

		before := time.Now()
		attempts(t, ctx, s, subject, ip, cfg.MaxFailures)
		st, err := s.Attempt(ctx, subject, ip)
		if err != nil || st == nil {
			t.Fatalf("Attempt over budget = %v, %v, want lock", st, err)
		}
		if st.FailedCount != cfg.MaxFailures {
			t.Fatalf("FailedCount = %d, want %d", st.FailedCount, cfg.MaxFailures)
		}
		minUntil := before.Add(cfg.LockDuration - time.Minute)
		maxUntil := time.Now().Add(cfg.LockDuration + time.Minute)
		if st.Until.Before(minUntil) || st.Until.After(maxUntil) {
			t.Fatalf("Until out of range: got %v, want in [%v, %v]", st.Until, minUntil, maxUntil)
		}

		// Попытка под блокировкой строку не пишет: ни счётчик, ни срок не растут.
		again, err := s.Attempt(ctx, subject, ip)
		if err != nil || again == nil {
			t.Fatalf("Attempt under lock = %v, %v, want lock", again, err)
		}
		if !again.Until.Equal(st.Until) || again.FailedCount != st.FailedCount {
			t.Fatalf("attempt under lock changed the row: %+v, was %+v", again, st)
		}

		// Бакеты (subject, ip) независимы.
		attempts(t, ctx, s, subject, "10.0.0.99", 1)
	})

	t.Run("locks after the only attempt when MaxFailures is 1", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 1
		cfg.LockDuration = 90 * time.Second
		s := newStore(t, db, cfg)

		attempts(t, ctx, s, "one@example.com", "10.0.0.9", 1)
		st, err := s.Attempt(ctx, "one@example.com", "10.0.0.9")
		if err != nil || st == nil || st.FailedCount != 1 {
			t.Fatalf("second Attempt = %+v, %v, want lock with FailedCount 1", st, err)
		}
		if remaining := time.Until(st.Until); remaining < 60*time.Second || remaining > 2*time.Minute {
			t.Fatalf("Until = %v (remaining %v), want ~90s", st.Until, remaining)
		}
	})

	// Блокировку решают часы базы: истёкший locked_until — новое окно.
	t.Run("lock uses database clock", func(t *testing.T) {
		s := newStore(t, db, config())
		const subject, ip = "clock@example.com", "10.0.0.3"
		if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, locked_until)
			VALUES ($1, $2, 3, NOW() + interval '1 hour')`, subject, ip); err != nil {
			t.Fatalf("insert locked: %v", err)
		}
		if st, err := s.Attempt(ctx, subject, ip); err != nil || st == nil {
			t.Fatalf("active lock missed: st=%v err=%v", st, err)
		}
		expireLock(t, ctx, db, subject, ip)
		attempts(t, ctx, s, subject, ip, 1)
		if n := failedCount(t, ctx, db, subject, ip); n != 1 {
			t.Fatalf("failed_count after expiry = %d, want 1 (new window)", n)
		}
	})

	t.Run("success resets history", func(t *testing.T) {
		s := newStore(t, db, config())
		const subject, ip = "phone:+15555550123", "10.0.0.2"

		attempts(t, ctx, s, subject, ip, 2)
		if err := s.RecordSuccess(ctx, subject, ip); err != nil {
			t.Fatalf("RecordSuccess: %v", err)
		}
		if n := failedCount(t, ctx, db, subject, ip); n != -1 {
			t.Fatalf("row survived RecordSuccess with failed_count %d", n)
		}
		attempts(t, ctx, s, subject, ip, 1)
		if n := failedCount(t, ctx, db, subject, ip); n != 1 {
			t.Fatalf("failed_count after reset = %d, want 1", n)
		}
	})

	// Успех на последней попытке окна снимает блокировку, которую она выставила.
	t.Run("success on the last attempt lifts the lock", func(t *testing.T) {
		cfg := config()
		s := newStore(t, db, cfg)
		const subject, ip = "last@example.com", "10.0.0.12"

		attempts(t, ctx, s, subject, ip, cfg.MaxFailures)
		if err := s.RecordSuccess(ctx, subject, ip); err != nil {
			t.Fatalf("RecordSuccess: %v", err)
		}
		attempts(t, ctx, s, subject, ip, 1)
	})

	// Истечение блокировки начинает новое окно: первая попытка после него
	// считается первой, а не N+1-й.
	t.Run("new window after lock expiry", func(t *testing.T) {
		cfg := config()
		s := newStore(t, db, cfg)
		const subject, ip = "window@example.com", "10.0.0.4"

		attempts(t, ctx, s, subject, ip, cfg.MaxFailures)
		expireLock(t, ctx, db, subject, ip)

		attempts(t, ctx, s, subject, ip, cfg.MaxFailures)
		st, err := s.Attempt(ctx, subject, ip)
		if err != nil || st == nil || st.FailedCount != cfg.MaxFailures {
			t.Fatalf("second window lock = %+v, %v, want FailedCount %d", st, err, cfg.MaxFailures)
		}
	})

	// Поднятие порога не снимает активные блокировки.
	t.Run("raising threshold keeps active lock", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 2
		const subject, ip = "threshold@example.com", "10.0.0.5"
		attempts(t, ctx, newStore(t, db, cfg), subject, ip, cfg.MaxFailures)

		raised := cfg
		raised.MaxFailures = 5
		if st, err := newStore(t, db, raised).Attempt(ctx, subject, ip); err != nil || st == nil {
			t.Fatalf("Attempt after raise = %v, %v: raising BRUTEFORCE_MAX_FAILURES must not lift an active lock", st, err)
		}
	})

	// Регрессия NF7: счётчик блокировок считает переход «блокировки не было →
	// есть», а не равенство порогу: после понижения BRUTEFORCE_MAX_FAILURES
	// строка со счётчиком 4 при пороге 3 блокируется на пятой попытке.
	t.Run("lowering threshold locks and counts the lock", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 5
		const subject, ip = "lowered@example.com", "10.0.0.6"
		attempts(t, ctx, newStore(t, db, cfg), subject, ip, 4)

		lowered := cfg
		lowered.MaxFailures = 3
		s := newStore(t, db, lowered)

		before := metrictest.Collect(t)
		st, err := s.Attempt(ctx, subject, ip)
		if err != nil || st == nil {
			t.Fatalf("Attempt after lowering = %v, %v: budget of the window is spent", st, err)
		}
		if st.FailedCount != 5 {
			t.Fatalf("FailedCount = %d, want 5", st.FailedCount)
		}
		if got := metrictest.Collect(t).Sub(before).Value("bruteforce_locks_total"); got != 1 {
			t.Fatalf("bruteforce_locks_total delta = %v, want 1", got)
		}

		before = metrictest.Collect(t)
		if _, err := s.Attempt(ctx, subject, ip); err != nil {
			t.Fatalf("Attempt over lock: %v", err)
		}
		if got := metrictest.Collect(t).Sub(before).Value("bruteforce_locks_total"); got != 0 {
			t.Fatalf("bruteforce_locks_total delta over an active lock = %v, want 0", got)
		}
	})

	// Разные записи одного адреса — один бакет счётчика.
	t.Run("canonical ip", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 2
		s := newStore(t, db, cfg)
		const subject = "canon@example.com"

		attempts(t, ctx, s, subject, "203.0.113.7", 1)
		attempts(t, ctx, s, subject, "::ffff:203.0.113.7", 1)
		if st, err := s.Attempt(ctx, subject, "203.0.113.7"); err != nil || st == nil {
			t.Fatalf("Attempt = %v, %v: IPv4-mapped IPv6 must share the bucket of the plain IPv4 address", st, err)
		}
		if n := failedCount(t, ctx, db, subject, "203.0.113.7"); n != 2 {
			t.Fatalf("failed_count = %d, want 2", n)
		}
	})

	// Регистр и пробелы subject'а — один бакет: иначе каждый вариант регистра
	// email давал бы новые попытки (SEC-05).
	t.Run("normalized subject", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 2
		s := newStore(t, db, cfg)
		const ip = "203.0.113.9"

		attempts(t, ctx, s, "Case@Example.com", ip, 1)
		attempts(t, ctx, s, " case@example.COM ", ip, 1)
		if st, err := s.Attempt(ctx, "CASE@EXAMPLE.COM", ip); err != nil || st == nil {
			t.Fatalf("Attempt = %v, %v: case and surrounding spaces of subject must not open a new bucket", st, err)
		}
		if n := failedCount(t, ctx, db, "case@example.com", ip); n != 2 {
			t.Fatalf("failed_count = %d, want 2", n)
		}
	})

	t.Run("ipv6 aggregated to prefix", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 2
		cfg.IPv6Prefix = 64
		s := newStore(t, db, cfg)
		const subject = "v6@example.com"

		attempts(t, ctx, s, subject, "2001:db8:0:1::1", 1)
		attempts(t, ctx, s, subject, "2001:db8:0:1:ffff::9", 1)
		if st, err := s.Attempt(ctx, subject, "2001:db8:0:1:abcd::1"); err != nil || st == nil {
			t.Fatalf("Attempt = %v, %v: addresses of one /64 must share a bucket at BRUTEFORCE_IPV6_PREFIX=64", st, err)
		}
		if n := failedCount(t, ctx, db, subject, "2001:db8:0:1::"); n != 2 {
			t.Fatalf("failed_count of /64 bucket = %d, want 2", n)
		}
	})

	t.Run("errors carry kit prefix", func(t *testing.T) {
		s := newStore(t, db, config())
		canceled, cancel := context.WithCancel(ctx)
		cancel()

		if _, err := s.Attempt(canceled, "u", "10.0.0.7"); !hasKitPrefix(err) {
			t.Fatalf("Attempt = %v, want bruteforcekit: prefix", err)
		}
		if err := s.RecordSuccess(canceled, "u", "10.0.0.7"); !hasKitPrefix(err) {
			t.Fatalf("RecordSuccess = %v, want bruteforcekit: prefix", err)
		}
		if err := s.Cleanup(canceled); !hasKitPrefix(err) {
			t.Fatalf("Cleanup = %v, want bruteforcekit: prefix", err)
		}
	})
}

// Регрессия SEC-05: попытка учитывается атомарно до проверки секрета, поэтому
// из N одновременных попыток пары проходят ровно MaxFailures, а не все N, как
// при «прочитал блокировку, потом записал неудачу».
func TestStore_ConcurrentAttempts(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	cfg.MaxFailures = 5
	s := newStore(t, db, cfg)

	const (
		subject, ip = "concurrent@example.com", "10.0.0.8"
		n           = 50
	)
	var (
		wg              sync.WaitGroup
		allowed, locked atomic.Int64
	)
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			st, err := s.Attempt(ctx, subject, ip)
			switch {
			case err != nil:
				errs <- err
			case st == nil:
				allowed.Add(1)
			default:
				locked.Add(1)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Attempt: %v", err)
	}

	if got := allowed.Load(); got != int64(cfg.MaxFailures) {
		t.Fatalf("allowed attempts = %d, want %d (locked %d)", got, cfg.MaxFailures, locked.Load())
	}
	if n := failedCount(t, ctx, db, subject, ip); n != cfg.MaxFailures {
		t.Fatalf("failed_count = %d, want %d", n, cfg.MaxFailures)
	}
}

func TestStore_Cleanup(t *testing.T) {
	db, ctx := startDB(t)

	t.Run("keeps fresh and locked rows", func(t *testing.T) {
		cfg := config()
		cfg.CleanupRetention = time.Hour
		s := newStore(t, db, cfg)

		attempts(t, ctx, s, "fresh@example.com", "1.1.1.1", 1)
		attempts(t, ctx, s, "locked@example.com", "2.2.2.2", cfg.MaxFailures)
		// Заблокированная строка старше retention — переживает Cleanup.
		if _, err := db.Exec(ctx, `UPDATE bruteforce_attempts SET last_attempt = NOW() - interval '2 hours'
			WHERE subject = 'locked@example.com'`); err != nil {
			t.Fatalf("age locked row: %v", err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt)
			VALUES ('stale@example.com', '3.3.3.3', 1, NOW() - interval '2 hours')`); err != nil {
			t.Fatalf("insert stale: %v", err)
		}

		if err := s.Cleanup(ctx); err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
		if n := failedCount(t, ctx, db, "fresh@example.com", "1.1.1.1"); n != 1 {
			t.Fatalf("fresh row deleted (failed_count=%d)", n)
		}
		if n := failedCount(t, ctx, db, "locked@example.com", "2.2.2.2"); n != cfg.MaxFailures {
			t.Fatalf("locked row deleted (failed_count=%d)", n)
		}
		if n := failedCount(t, ctx, db, "stale@example.com", "3.3.3.3"); n != -1 {
			t.Fatalf("stale row survived Cleanup (failed_count=%d)", n)
		}
	})

	// Больше строк, чем в одном батче: Cleanup крутит DELETE до конца.
	t.Run("deletes in batches", func(t *testing.T) {
		cfg := config()
		cfg.CleanupRetention = time.Hour
		s := newStore(t, db, cfg)

		const rows = pgstore.CleanupBatchSize + 10
		if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt)
			SELECT 'batch-' || i, '4.4.4.4', 1, NOW() - interval '2 hours' FROM generate_series(1, $1) AS i`, rows); err != nil {
			t.Fatalf("insert batch rows: %v", err)
		}

		if err := s.Cleanup(ctx); err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
		var left int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM bruteforce_attempts WHERE subject LIKE 'batch-%'`).Scan(&left); err != nil {
			t.Fatalf("count: %v", err)
		}
		if left != 0 {
			t.Fatalf("%d rows left after Cleanup", left)
		}
	})

	t.Run("job runs the same cleanup", func(t *testing.T) {
		cfg := config()
		cfg.CleanupRetention = time.Hour
		job := pgstore.CleanupJob(newStore(t, db, cfg), cfg)

		if job.Name != "bruteforcekit.cleanup" || job.Interval != cfg.CleanupInterval {
			t.Fatalf("job = %+v", job)
		}
		if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt)
			VALUES ('job-stale@example.com', '6.6.6.6', 1, NOW() - interval '2 hours')`); err != nil {
			t.Fatalf("insert stale: %v", err)
		}
		if err := job.Run(ctx); err != nil {
			t.Fatalf("job.Run: %v", err)
		}
		if n := failedCount(t, ctx, db, "job-stale@example.com", "6.6.6.6"); n != -1 {
			t.Fatalf("stale row survived job (failed_count=%d)", n)
		}
	})

	// Индекс миграции 0002 обслуживает предикат Cleanup.
	t.Run("last_attempt index exists", func(t *testing.T) {
		var idx int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'bruteforce_attempts_last_attempt_idx'`).Scan(&idx); err != nil {
			t.Fatalf("pg_indexes: %v", err)
		}
		if idx != 1 {
			t.Fatal("bruteforce_attempts_last_attempt_idx missing: миграция 0002 не применилась")
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'bruteforce_attempts_locked_idx'`).Scan(&idx); err != nil {
			t.Fatalf("pg_indexes: %v", err)
		}
		if idx != 0 {
			t.Fatal("bruteforce_attempts_locked_idx must be dropped by миграция 0002")
		}
	})
}

func failedCount(t *testing.T, ctx context.Context, db *pgkit.DB, subject, ip string) int {
	t.Helper()
	var n int
	err := db.QueryRow(ctx, `SELECT failed_count FROM bruteforce_attempts WHERE subject = $1 AND ip = $2`, subject, ip).Scan(&n)
	if pgkit.IsNoRows(err) {
		return -1
	}
	if err != nil {
		t.Fatalf("failed_count: %v", err)
	}
	return n
}

func expireLock(t *testing.T, ctx context.Context, db *pgkit.DB, subject, ip string) {
	t.Helper()
	if _, err := db.Exec(ctx, `UPDATE bruteforce_attempts SET locked_until = NOW() - interval '1 second'
		WHERE subject = $1 AND ip = $2`, subject, ip); err != nil {
		t.Fatalf("expire lock: %v", err)
	}
}

func hasKitPrefix(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "bruteforcekit/pgstore: ")
}
