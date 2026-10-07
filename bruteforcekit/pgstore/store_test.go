package pgstore_test

import (
	"context"
	"errors"
	"fmt"
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
		MaxFailures:         3,
		LockDuration:        15 * time.Minute,
		SubjectMaxFailures:  100,
		SubjectLockDuration: time.Minute,
		CleanupRetention:    24 * time.Hour,
		CleanupInterval:     5 * time.Minute,
		IPv6Prefix:          bruteforcekit.FullIPv6Prefix,
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
	s, err := pgstore.NewStore(cfg, db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestNewStore_ValidatesConfig(t *testing.T) {
	t.Parallel()
	// Нулевой конфиг выключил бы guard молча: locked_until = NOW() + 0s.
	if _, err := pgstore.NewStore(bruteforcekit.Config{}, nil); !errors.Is(err, bruteforcekit.ErrConfig) {
		t.Fatalf("NewStore(zero config) = %v, want ErrConfig", err)
	}
	cfg := config()
	cfg.LockDuration = 0
	if _, err := pgstore.NewStore(cfg, nil); !errors.Is(err, bruteforcekit.ErrConfig) {
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
	if err := s.RecordSuccess(ctx, "u\x00@example.com", "203.0.113.7"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("RecordSuccess(subject with NUL) = %v, want ErrInvalidKey", err)
	}
	if _, err := s.Attempt(ctx, "u\x00@example.com", "203.0.113.7"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("Attempt(subject with NUL) = %v, want ErrInvalidKey", err)
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

// Регрессия KSEC-04 (E11): проверок пароля одного subject'а за окно не больше
// SubjectMaxFailures при любом числе адресов — и IPv4, и /64 одной /48. Под блокировкой
// субъекта пара не учитывается, блокировка субъекта короткая и по часам базы, успех снимает её
// окно, а истечение начинает новое.
func TestStore_SubjectBudget(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	cfg.MaxFailures, cfg.SubjectMaxFailures = 5, 10
	s := newStore(t, db, cfg)

	spray := func(subject string, ips []string) (allowed int, last *bruteforcekit.LockState) {
		for _, ip := range ips {
			for range cfg.MaxFailures + 3 {
				st, err := s.Attempt(ctx, subject, ip)
				if err != nil {
					t.Fatalf("Attempt(%s): %v", ip, err)
				}
				if st == nil {
					allowed++
				} else {
					last = st
				}
			}
		}
		return allowed, last
	}
	v4, v6 := make([]string, 0, 20), make([]string, 0, 20)
	for i := range 20 {
		v4 = append(v4, fmt.Sprintf("203.0.113.%d", i+1))
		v6 = append(v6, fmt.Sprintf("2001:db8:ab:%x::1", i))
	}
	for subject, ips := range map[string][]string{"victim4@example.com": v4, "victim6@example.com": v6} {
		allowed, last := spray(subject, ips)
		if allowed != cfg.SubjectMaxFailures || last == nil || last.Scope != bruteforcekit.ScopeSubject {
			t.Fatalf("%s: %d password checks, last lock %+v; want %d and a subject lock", subject, allowed, last, cfg.SubjectMaxFailures)
		}
		if last.RetryAfter <= 0 || last.RetryAfter > cfg.SubjectLockDuration {
			t.Fatalf("%s: RetryAfter = %s, want within SubjectLockDuration %s", subject, last.RetryAfter, cfg.SubjectLockDuration)
		}
	}
	// 10 попыток прошли с двух адресов; под блокировкой субъекта третий адрес строки не завёл.
	if n := failedCount(t, ctx, db, "victim4@example.com", "203.0.113.3"); n != -1 {
		t.Fatalf("pair counted under the subject lock: failed_count = %d", n)
	}

	// Истечение блокировки субъекта — новое окно; успех снимает его сразу.
	expireLock(t, ctx, db, "victim4@example.com", "")
	attempts(t, ctx, s, "victim4@example.com", "203.0.113.30", 1)
	if n := failedCount(t, ctx, db, "victim4@example.com", ""); n != 1 {
		t.Fatalf("subject window after the lock expired: failed_count = %d, want 1", n)
	}
	if err := s.RecordSuccess(ctx, "victim4@example.com", "203.0.113.30"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	if n := failedCount(t, ctx, db, "victim4@example.com", ""); n != 0 {
		t.Fatalf("success left the subject window: failed_count = %d, want 0", n)
	}
	if n := failedCount(t, ctx, db, "victim4@example.com", "203.0.113.30"); n != -1 {
		t.Fatalf("success left the pair row: failed_count = %d", n)
	}

	// Под блокировкой пары субъект не учитывается.
	attempts(t, ctx, s, "pair@example.com", "198.51.100.1", cfg.MaxFailures)
	if st, err := s.Attempt(ctx, "pair@example.com", "198.51.100.1"); err != nil || st == nil || st.Scope != bruteforcekit.ScopePair {
		t.Fatalf("Attempt under the pair lock = %+v, %v, want a pair lock", st, err)
	}
	if n := failedCount(t, ctx, db, "pair@example.com", ""); n != cfg.MaxFailures {
		t.Fatalf("subject counted under the pair lock: failed_count = %d, want %d", n, cfg.MaxFailures)
	}
}

// Бюджет субъекта не превышается и под одновременными попытками с разных адресов.
func TestStore_SubjectBudgetConcurrent(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	cfg.SubjectMaxFailures = 7
	s := newStore(t, db, cfg)

	const subject, n = "spray@example.com", 40
	var (
		wg      sync.WaitGroup
		allowed atomic.Int64
	)
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			st, err := s.Attempt(ctx, subject, fmt.Sprintf("192.0.2.%d", i+1))
			switch {
			case err != nil:
				errs <- err
			case st == nil:
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Attempt: %v", err)
	}
	if got := allowed.Load(); got != int64(cfg.SubjectMaxFailures) {
		t.Fatalf("allowed attempts = %d, want %d", got, cfg.SubjectMaxFailures)
	}
}

// Попытка, которая ждала строку субъекта, пока другой вход блокировал его, видит блокировку и
// пару не учитывает: строка субъекта читается FOR UPDATE до учёта пары.
func TestStore_SubjectLockSeenAfterWaiting(t *testing.T) {
	pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, tr := pg.DB(t), pg.Transactor(t)
	s := newStore(t, db, config())
	const subject, n = "waiting@example.com", 3 // пул testkit — 5 соединений: попытки, транзакция и опрос
	if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count) VALUES ($1, '', 1)`, subject); err != nil {
		t.Fatalf("seed the subject row: %v", err)
	}

	locks := make(chan *bruteforcekit.LockState, n)
	err := tr.WithTx(ctx, func(txCtx context.Context) error {
		if _, err := db.Exec(txCtx, `SELECT 1 FROM bruteforce_attempts WHERE subject = $1 AND ip = '' FOR UPDATE`, subject); err != nil {
			return err
		}
		for i := range n {
			go func() {
				st, err := s.Attempt(ctx, subject, fmt.Sprintf("192.0.2.%d", i+1))
				if err != nil {
					t.Errorf("Attempt: %v", err)
				}
				locks <- st
			}()
		}
		for waiting := 0; waiting < n; {
			if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(5 * time.Millisecond)
		}
		_, err := db.Exec(txCtx, `UPDATE bruteforce_attempts SET locked_until = NOW() + interval '1 minute' WHERE subject = $1 AND ip = ''`, subject)
		return err
	})
	if err != nil {
		t.Fatalf("hold the subject row: %v", err)
	}
	for range n {
		if st := <-locks; st == nil || st.Scope != bruteforcekit.ScopeSubject {
			t.Fatalf("Attempt that waited for the subject row = %+v, want the subject lock", st)
		}
	}
	var pairs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM bruteforce_attempts WHERE subject = $1 AND ip <> ''`, subject).Scan(&pairs); err != nil {
		t.Fatal(err)
	}
	if pairs != 0 {
		t.Fatalf("pair rows = %d, want none: attempts under the subject lock counted their pairs", pairs)
	}
}

// Понижение SubjectMaxFailures блокирует subject, чей счётчик уже выше нового порога, следующей
// же попыткой, как у пары.
func TestStore_SubjectBudgetLowered(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	cfg.SubjectMaxFailures = 10
	for i := range 4 {
		attempts(t, ctx, newStore(t, db, cfg), "lowered@example.com", fmt.Sprintf("192.0.2.%d", i+1), 1)
	}
	cfg.SubjectMaxFailures = 3
	st, err := newStore(t, db, cfg).Attempt(ctx, "lowered@example.com", "192.0.2.9")
	if err != nil || st == nil || st.Scope != bruteforcekit.ScopeSubject || st.FailedCount != 5 {
		t.Fatalf("Attempt over the lowered budget = %+v, %v, want a subject lock with FailedCount 5", st, err)
	}
}

// SubjectBudgetOff — учёт только по паре: строки субъекта нет, каждый адрес даёт MaxFailures.
func TestStore_SubjectBudgetOff(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	cfg.SubjectMaxFailures = bruteforcekit.SubjectBudgetOff
	s := newStore(t, db, cfg)
	for i := range 3 {
		attempts(t, ctx, s, "free@example.com", fmt.Sprintf("192.0.2.%d", i+1), cfg.MaxFailures)
	}
	if n := failedCount(t, ctx, db, "free@example.com", ""); n != -1 {
		t.Fatalf("subject row with the budget off: failed_count = %d", n)
	}
}

// RetryAfter — остаток по часам базы: сервис с убежавшими часами отдаёт тот же Retry-After.
func TestStore_RetryAfterByDatabaseClock(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	s := newStore(t, db, cfg)
	const subject, ip = "clock@example.com", "10.0.0.20"
	attempts(t, ctx, s, subject, ip, cfg.MaxFailures)
	if _, err := db.Exec(ctx, `UPDATE bruteforce_attempts SET locked_until = NOW() + interval '90 seconds'
		WHERE subject = $1 AND ip = $2`, subject, ip); err != nil {
		t.Fatalf("set lock: %v", err)
	}
	st, err := s.Attempt(ctx, subject, ip)
	if err != nil || st == nil || st.RetryAfter <= 80*time.Second || st.RetryAfter > 90*time.Second {
		t.Fatalf("Attempt = %+v, %v; want RetryAfter just under 90s", st, err)
	}
}

// Регрессия bruteforcekit-subject-255: subject длиннее MaxSubjectLen учитывается по SHA-256, а не
// отказом — обход в адаптере не нужен.
func TestStore_LongSubjectIsCounted(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	s := newStore(t, db, cfg)
	long := strings.Repeat("a", 300) + "@example.com"
	attempts(t, ctx, s, long, "10.0.0.21", cfg.MaxFailures)
	if st, err := s.Attempt(ctx, long, "10.0.0.21"); err != nil || st == nil {
		t.Fatalf("Attempt over budget = %+v, %v, want lock", st, err)
	}
	var key string
	if err := db.QueryRow(ctx, `SELECT subject FROM bruteforce_attempts WHERE ip = '10.0.0.21'`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "SHA256:") {
		t.Fatalf("stored subject = %q, want the SHA-256 key", key)
	}
}

// Регрессия bruteforcekit-attempt-retry-test: блокировка, не пустившая upsert, снялась до чтения
// (успех параллельного входа) — попытка учитывается заново, в новом окне.
func TestStore_AttemptRecountsWhenLockVanishes(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	s := newStore(t, db, cfg)
	const subject, ip = "race@example.com", "10.0.0.22"
	attempts(t, ctx, s, subject, ip, cfg.MaxFailures)

	var seen atomic.Int32
	pgstore.SetLockSeen(s, func() {
		if seen.Add(1) == 1 {
			if err := s.RecordSuccess(ctx, subject, ip); err != nil {
				t.Errorf("RecordSuccess: %v", err)
			}
		}
	})
	if st, err := s.Attempt(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("Attempt after the lock vanished = %+v, %v, want allowed", st, err)
	}
	if seen.Load() != 1 || failedCount(t, ctx, db, subject, ip) != 1 {
		t.Fatalf("lock seen %d times, failed_count %d; want one recount into a new window", seen.Load(), failedCount(t, ctx, db, subject, ip))
	}
}

// RetryAfter — остаток до срока по часам базы, а не сервиса: часы базы далеко от часов процесса.
func TestStore_RetryAfterUsesDatabaseNow(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil, config())
	dbNow := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := pgstore.RetryAfter(s, dbNow.Add(90*time.Second), dbNow); got != 90*time.Second {
		t.Fatalf("RetryAfter = %s, want 90s by the database clock", got)
	}
	if got := pgstore.RetryAfter(s, dbNow, dbNow.Add(time.Second)); got != 0 {
		t.Fatalf("RetryAfter of a passed lock = %s, want 0", got)
	}
}

// Бюджет субъекта меньше числа одновременных первых попыток: строки субъекта ещё нет, и каждая
// попытка ждёт её блокировку, а не учитывает пару в обход бюджета (C13 валидатора C).
func TestStore_SubjectBudgetTinyConcurrent(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	cfg.SubjectMaxFailures = 2
	s := newStore(t, db, cfg)
	for round := range 20 {
		subject := fmt.Sprintf("tiny%d@example.com", round)
		var (
			wg      sync.WaitGroup
			allowed atomic.Int64
		)
		start := make(chan struct{})
		for i := range 4 {
			wg.Go(func() {
				<-start
				st, err := s.Attempt(ctx, subject, fmt.Sprintf("192.0.2.%d", i+1))
				if err != nil {
					t.Errorf("Attempt: %v", err)
				}
				if st == nil {
					allowed.Add(1)
				}
			})
		}
		close(start)
		wg.Wait()
		if got := allowed.Load(); got != int64(cfg.SubjectMaxFailures) {
			t.Fatalf("round %d: allowed %d of 4 first attempts, want the budget %d", round, got, cfg.SubjectMaxFailures)
		}
	}
}

// Регрессия B2 валидатора C: Attempt, RecordSuccess и Cleanup берут строки субъекта и пары в одном
// порядке (Cleanup занятые пропускает) — дедлоков (40P01) и других ошибок нет ни у одной операции.
func TestStore_NoDeadlocks(t *testing.T) {
	db, _ := startDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg := config()
	cfg.MaxFailures, cfg.SubjectMaxFailures = 100000, 100000
	s := newStore(t, db, cfg)
	var (
		mu   sync.Mutex
		errs []error
	)
	note := func(err error) {
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}

	// Вход пары вперемешку с попытками той же пары.
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Go(func() {
			for i := range 150 {
				if (w+i)%2 == 0 {
					_, err := s.Attempt(ctx, "mix@example.com", "10.1.1.1")
					note(err)
				} else {
					note(s.RecordSuccess(ctx, "mix@example.com", "10.1.1.1"))
				}
			}
		})
	}
	// Пользователь входит (попытка, затем успех), два клиента той же пары шлют попытки.
	wg.Go(func() {
		for range 100 {
			_, err := s.Attempt(ctx, "user@example.com", "10.2.2.2")
			note(err)
			note(s.RecordSuccess(ctx, "user@example.com", "10.2.2.2"))
		}
	})
	for range 2 {
		wg.Go(func() {
			for range 100 {
				_, err := s.Attempt(ctx, "user@example.com", "10.2.2.2")
				note(err)
			}
		})
	}
	wg.Wait()

	// Cleanup по старым строкам (пара вставлена раньше субъекта), параллельно — попытки по ним.
	const old = 1500
	for range 2 {
		if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt)
			SELECT 'old' || g || '@example.com', v.ip, 1, NOW() - interval '3 days'
			FROM generate_series(1, $1) g, (VALUES (1, '10.3.3.3'), (2, '')) AS v(o, ip) ORDER BY g, v.o
			ON CONFLICT (subject, ip) DO UPDATE SET last_attempt = NOW() - interval '3 days', locked_until = NULL`, old); err != nil {
			t.Fatal(err)
		}
		wg.Go(func() { note(s.Cleanup(ctx)) })
		for w := range 3 {
			wg.Go(func() {
				for i := w; i < old; i += 3 {
					_, err := s.Attempt(ctx, fmt.Sprintf("old%d@example.com", i+1), "10.3.3.3")
					note(err)
				}
			})
		}
		wg.Wait()
	}
	for _, err := range errs {
		t.Errorf("operation failed: %v", err)
	}
}

// waitLockWaiters ждёт n сессий, стоящих на блокировке строки.
func waitLockWaiters(t *testing.T, ctx context.Context, db *pgkit.DB, n int) {
	t.Helper()
	for {
		var waiting int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting); err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if waiting >= n {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("no session waits for a lock: %v", ctx.Err())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// RecordSuccess в транзакции вызывающего держит строки до коммита — в том же порядке, что и
// Attempt (субъект, затем пара): попытка, пришедшая между двумя операторами, ждёт, а не замыкает
// цикл ожиданий.
func TestStore_RecordSuccessInTxKeepsLockOrder(t *testing.T) {
	pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, tr := pg.DB(t), pg.Transactor(t)
	s := newStore(t, db, config())
	const subject, ip = "order@example.com", "10.4.4.4"
	attempts(t, ctx, s, subject, ip, 1)

	attempted := make(chan error, 1)
	pgstore.SetSubjectReset(s, func() {
		go func() {
			_, err := s.Attempt(ctx, subject, ip)
			attempted <- err
		}()
		waitLockWaiters(t, ctx, db, 1)
	})
	if err := tr.WithTx(ctx, func(txCtx context.Context) error { return s.RecordSuccess(txCtx, subject, ip) }); err != nil {
		t.Fatalf("RecordSuccess in a transaction: %v", err)
	}
	if err := <-attempted; err != nil {
		t.Fatalf("Attempt during RecordSuccess in a transaction: %v", err)
	}
}

// Cleanup не ждёт строк, занятых попыткой: занятую строку пропускает, остальные удаляет.
func TestStore_CleanupSkipsBusyRows(t *testing.T) {
	pg := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, tr := pg.DB(t), pg.Transactor(t)
	s := newStore(t, db, config())
	if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt)
		VALUES ('busy@example.com', '', 1, NOW() - interval '3 days'), ('stale@example.com', '', 1, NOW() - interval '3 days')`); err != nil {
		t.Fatal(err)
	}
	err := tr.WithTx(ctx, func(txCtx context.Context) error {
		if _, err := db.Exec(txCtx, `SELECT 1 FROM bruteforce_attempts WHERE subject = 'busy@example.com' FOR UPDATE`); err != nil {
			return err
		}
		cleanCtx, cancelClean := context.WithTimeout(ctx, 5*time.Second)
		defer cancelClean()
		return s.Cleanup(cleanCtx)
	})
	if err != nil {
		t.Fatalf("Cleanup while a row is busy: %v", err)
	}
	if failedCount(t, ctx, db, "busy@example.com", "") != 1 || failedCount(t, ctx, db, "stale@example.com", "") != -1 {
		t.Fatal("Cleanup must skip the busy row and delete the stale one")
	}
}
