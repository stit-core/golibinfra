package pgstore_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/bruteforcekit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
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
	if _, err := s.Check(ctx, "", "203.0.113.7"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("Check(empty subject) = %v, want ErrInvalidKey", err)
	}
	if _, err := s.RecordFailure(ctx, "u", "not-an-ip"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("RecordFailure(bad ip) = %v, want ErrInvalidKey", err)
	}
	if err := s.RecordSuccess(ctx, strings.Repeat("s", bruteforcekit.MaxSubjectLen+1), "203.0.113.7"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("RecordSuccess(long subject) = %v, want ErrInvalidKey", err)
	}
}

func TestStore_Guard(t *testing.T) {
	db, ctx := startDB(t)

	t.Run("locks after max failures", func(t *testing.T) {
		cfg := config()
		s := newStore(t, db, cfg)
		const subject, ip = "user@example.com", "10.0.0.1"

		if st, err := s.Check(ctx, subject, ip); err != nil || st != nil {
			t.Fatalf("initial Check: st=%v err=%v", st, err)
		}

		for i := range cfg.MaxFailures - 1 {
			st, err := s.RecordFailure(ctx, subject, ip)
			if err != nil {
				t.Fatalf("RecordFailure %d: %v", i, err)
			}
			if st != nil {
				t.Fatalf("RecordFailure %d returned lock %v before threshold", i, st)
			}
		}
		if st, err := s.Check(ctx, subject, ip); err != nil || st != nil {
			t.Fatalf("Check below threshold: st=%v err=%v", st, err)
		}

		before := time.Now()
		st, err := s.RecordFailure(ctx, subject, ip)
		if err != nil {
			t.Fatalf("RecordFailure threshold: %v", err)
		}
		if st == nil {
			t.Fatal("RecordFailure at threshold must return lock state")
		}
		if st.FailedCount != cfg.MaxFailures {
			t.Fatalf("FailedCount = %d, want %d", st.FailedCount, cfg.MaxFailures)
		}
		minUntil := before.Add(cfg.LockDuration - time.Minute)
		maxUntil := time.Now().Add(cfg.LockDuration + time.Minute)
		if st.Until.Before(minUntil) || st.Until.After(maxUntil) {
			t.Fatalf("Until out of range: got %v, want in [%v, %v]", st.Until, minUntil, maxUntil)
		}

		checked, err := s.Check(ctx, subject, ip)
		if err != nil || checked == nil {
			t.Fatalf("Check after lock: st=%v err=%v", checked, err)
		}
		if !checked.Until.Equal(st.Until) || checked.FailedCount != st.FailedCount {
			t.Fatalf("Check = %+v, RecordFailure = %+v", checked, st)
		}

		// Бакеты (subject, ip) независимы.
		if st, err := s.Check(ctx, subject, "10.0.0.99"); err != nil || st != nil {
			t.Fatalf("other ip must not be locked: st=%v err=%v", st, err)
		}
	})

	t.Run("locks on first failure when MaxFailures is 1", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 1
		cfg.LockDuration = 90 * time.Second
		s := newStore(t, db, cfg)

		st, err := s.RecordFailure(ctx, "one@example.com", "10.0.0.9")
		if err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		if st == nil || st.FailedCount != 1 {
			t.Fatalf("RecordFailure = %+v, want lock with FailedCount 1", st)
		}
		if remaining := time.Until(st.Until); remaining < 60*time.Second || remaining > 2*time.Minute {
			t.Fatalf("Until = %v (remaining %v), want ~90s", st.Until, remaining)
		}
	})

	// Check решает по часам базы: истёкший locked_until — не блокировка.
	t.Run("check uses database clock", func(t *testing.T) {
		s := newStore(t, db, config())
		const subject, ip = "clock@example.com", "10.0.0.3"
		if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, locked_until)
			VALUES ($1, $2, 1, NOW() - interval '1 second')`, subject, ip); err != nil {
			t.Fatalf("insert expired: %v", err)
		}
		if st, err := s.Check(ctx, subject, ip); err != nil || st != nil {
			t.Fatalf("expired lock reported: st=%v err=%v", st, err)
		}
		if _, err := db.Exec(ctx, `UPDATE bruteforce_attempts SET locked_until = NOW() + interval '1 hour'
			WHERE subject = $1 AND ip = $2`, subject, ip); err != nil {
			t.Fatalf("update future: %v", err)
		}
		if st, err := s.Check(ctx, subject, ip); err != nil || st == nil {
			t.Fatalf("active lock missed: st=%v err=%v", st, err)
		}
	})

	t.Run("success resets history", func(t *testing.T) {
		s := newStore(t, db, config())
		const subject, ip = "phone:+15555550123", "10.0.0.2"

		for i := range 2 {
			if _, err := s.RecordFailure(ctx, subject, ip); err != nil {
				t.Fatalf("RecordFailure %d: %v", i, err)
			}
		}
		if err := s.RecordSuccess(ctx, subject, ip); err != nil {
			t.Fatalf("RecordSuccess: %v", err)
		}
		if n := failedCount(t, ctx, db, subject, ip); n != -1 {
			t.Fatalf("row survived RecordSuccess with failed_count %d", n)
		}
		if _, err := s.RecordFailure(ctx, subject, ip); err != nil {
			t.Fatalf("RecordFailure after reset: %v", err)
		}
		if n := failedCount(t, ctx, db, subject, ip); n != 1 {
			t.Fatalf("failed_count after reset = %d, want 1", n)
		}
	})

	// Истечение блокировки начинает новое окно: первая неудача после него
	// считается первой, а не N+1-й.
	t.Run("new window after lock expiry", func(t *testing.T) {
		cfg := config()
		s := newStore(t, db, cfg)
		const subject, ip = "window@example.com", "10.0.0.4"

		for range cfg.MaxFailures {
			if _, err := s.RecordFailure(ctx, subject, ip); err != nil {
				t.Fatalf("RecordFailure: %v", err)
			}
		}
		expireLock(t, ctx, db, subject, ip)

		st, err := s.RecordFailure(ctx, subject, ip)
		if err != nil {
			t.Fatalf("RecordFailure after expiry: %v", err)
		}
		if st != nil {
			t.Fatalf("failure after expiry locked again: %+v", st)
		}
		if n := failedCount(t, ctx, db, subject, ip); n != 1 {
			t.Fatalf("failed_count after expiry = %d, want 1 (new window)", n)
		}

		for range cfg.MaxFailures - 2 {
			if _, err := s.RecordFailure(ctx, subject, ip); err != nil {
				t.Fatalf("RecordFailure: %v", err)
			}
		}
		st, err = s.RecordFailure(ctx, subject, ip)
		if err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		if st == nil || st.FailedCount != cfg.MaxFailures {
			t.Fatalf("second window lock = %+v, want FailedCount %d", st, cfg.MaxFailures)
		}
	})

	// Поднятие порога не снимает активные блокировки.
	t.Run("raising threshold keeps active lock", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 2
		const subject, ip = "threshold@example.com", "10.0.0.5"
		s := newStore(t, db, cfg)
		for range cfg.MaxFailures {
			if _, err := s.RecordFailure(ctx, subject, ip); err != nil {
				t.Fatalf("RecordFailure: %v", err)
			}
		}

		raised := cfg
		raised.MaxFailures = 5
		s = newStore(t, db, raised)
		st, err := s.RecordFailure(ctx, subject, ip)
		if err != nil {
			t.Fatalf("RecordFailure after raise: %v", err)
		}
		if st == nil {
			t.Fatal("raising BRUTEFORCE_MAX_FAILURES must not lift an active lock")
		}
		if st, err := s.Check(ctx, subject, ip); err != nil || st == nil {
			t.Fatalf("Check after raise: st=%v err=%v", st, err)
		}
	})

	// Регрессия NF7: счётчик блокировок считает переход «блокировки не было →
	// есть». При равенстве failed_count порогу понижение BRUTEFORCE_MAX_FAILURES
	// между деплоями теряло бы security-сигнал: строка со счётчиком 4 при новом
	// пороге 3 блокируется на пятой неудаче, а 5 != 3.
	t.Run("lowering threshold still counts the lock", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 5
		const subject, ip = "lowered@example.com", "10.0.0.6"
		s := newStore(t, db, cfg)
		for range 4 {
			if st, err := s.RecordFailure(ctx, subject, ip); err != nil || st != nil {
				t.Fatalf("RecordFailure below threshold: st=%v err=%v", st, err)
			}
		}

		lowered := cfg
		lowered.MaxFailures = 3
		s = newStore(t, db, lowered)

		before := snapshot(t)
		st, err := s.RecordFailure(ctx, subject, ip)
		if err != nil || st == nil {
			t.Fatalf("RecordFailure after lowering: st=%v err=%v", st, err)
		}
		if st.FailedCount != 5 {
			t.Fatalf("FailedCount = %d, want 5", st.FailedCount)
		}
		if got := snapshot(t).locks - before.locks; got != 1 {
			t.Fatalf("bruteforce_locks_total delta = %d, want 1", got)
		}

		// Неудача поверх активной блокировки продлевает её, но перехода нет.
		before = snapshot(t)
		if _, err := s.RecordFailure(ctx, subject, ip); err != nil {
			t.Fatalf("RecordFailure over lock: %v", err)
		}
		if got := snapshot(t).locks - before.locks; got != 0 {
			t.Fatalf("bruteforce_locks_total delta over an active lock = %d, want 0", got)
		}
	})

	// Разные записи одного адреса — один бакет счётчика.
	t.Run("canonical ip", func(t *testing.T) {
		cfg := config()
		cfg.MaxFailures = 2
		s := newStore(t, db, cfg)
		const subject = "canon@example.com"

		if _, err := s.RecordFailure(ctx, subject, "203.0.113.7"); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		st, err := s.RecordFailure(ctx, subject, "::ffff:203.0.113.7")
		if err != nil {
			t.Fatalf("RecordFailure mapped: %v", err)
		}
		if st == nil {
			t.Fatal("IPv4-mapped IPv6 must share the bucket of the plain IPv4 address")
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

		if _, err := s.RecordFailure(ctx, "Case@Example.com", ip); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		st, err := s.RecordFailure(ctx, " case@example.COM ", ip)
		if err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		if st == nil {
			t.Fatal("case and surrounding spaces of subject must not open a new bucket")
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

		if _, err := s.RecordFailure(ctx, subject, "2001:db8:0:1::1"); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		st, err := s.RecordFailure(ctx, subject, "2001:db8:0:1:ffff::9")
		if err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		if st == nil {
			t.Fatal("addresses of one /64 must share a bucket at BRUTEFORCE_IPV6_PREFIX=64")
		}
		if n := failedCount(t, ctx, db, subject, "2001:db8:0:1::"); n != 2 {
			t.Fatalf("failed_count of /64 bucket = %d, want 2", n)
		}
	})

	t.Run("errors carry kit prefix", func(t *testing.T) {
		s := newStore(t, db, config())
		canceled, cancel := context.WithCancel(ctx)
		cancel()

		if _, err := s.Check(canceled, "u", "10.0.0.7"); !hasKitPrefix(err) {
			t.Fatalf("Check = %v, want bruteforcekit: prefix", err)
		}
		if _, err := s.RecordFailure(canceled, "u", "10.0.0.7"); !hasKitPrefix(err) {
			t.Fatalf("RecordFailure = %v, want bruteforcekit: prefix", err)
		}
		if err := s.RecordSuccess(canceled, "u", "10.0.0.7"); !hasKitPrefix(err) {
			t.Fatalf("RecordSuccess = %v, want bruteforcekit: prefix", err)
		}
		if err := s.Cleanup(canceled); !hasKitPrefix(err) {
			t.Fatalf("Cleanup = %v, want bruteforcekit: prefix", err)
		}
	})
}

// Счётчик атомарен: конкурентные неудачи не теряются в read-modify-write.
func TestStore_ConcurrentRecordFailure(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	cfg.MaxFailures = 1000
	s := newStore(t, db, cfg)

	const (
		subject, ip = "concurrent@example.com", "10.0.0.8"
		attempts    = 50
	)
	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for range attempts {
		wg.Go(func() {
			if _, err := s.RecordFailure(ctx, subject, ip); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("RecordFailure: %v", err)
	}

	if n := failedCount(t, ctx, db, subject, ip); n != attempts {
		t.Fatalf("failed_count = %d, want %d", n, attempts)
	}
}

func TestStore_Cleanup(t *testing.T) {
	db, ctx := startDB(t)

	t.Run("keeps fresh and locked rows", func(t *testing.T) {
		cfg := config()
		cfg.CleanupRetention = time.Hour
		s := newStore(t, db, cfg)

		if _, err := s.RecordFailure(ctx, "fresh@example.com", "1.1.1.1"); err != nil {
			t.Fatalf("RecordFailure fresh: %v", err)
		}
		for range cfg.MaxFailures {
			if _, err := s.RecordFailure(ctx, "locked@example.com", "2.2.2.2"); err != nil {
				t.Fatalf("RecordFailure locked: %v", err)
			}
		}
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
	return err != nil && strings.HasPrefix(err.Error(), "bruteforcekit: ")
}
