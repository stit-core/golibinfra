package pgstore_test

import (
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func TestStore_Metrics(t *testing.T) {
	db, ctx := startDB(t)
	cfg := config()
	cfg.MaxFailures = 2
	cfg.SubjectMaxFailures = 3
	cfg.CleanupRetention = time.Hour
	before := metrictest.Collect(t)
	s := newStore(t, db, cfg)

	const subject, ip = "metrics@example.com", "10.0.0.10"

	attempts(t, ctx, s, subject, ip, cfg.MaxFailures)
	// Попытки под активной блокировкой — locked, но не новый lock.
	for range 2 {
		if st, err := s.Attempt(ctx, subject, ip); err != nil || st == nil {
			t.Fatalf("Attempt under lock = %v, %v, want lock", st, err)
		}
	}
	// Третья попытка subject'а с другого адреса исчерпывает бюджет субъекта, четвёртая упирается в него.
	attempts(t, ctx, s, subject, "10.0.0.11", 1)
	if st, err := s.Attempt(ctx, subject, "10.0.0.12"); err != nil || st == nil {
		t.Fatalf("Attempt under the subject lock = %v, %v, want lock", st, err)
	}

	if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, last_attempt)
		VALUES ('metrics-stale@example.com', '5.5.5.5', 1, NOW() - interval '2 hours')`); err != nil {
		t.Fatalf("insert stale: %v", err)
	}
	if err := s.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	d := metrictest.Collect(t).Sub(before)
	for _, want := range []struct {
		name   string
		labels []string
		value  float64
	}{
		{"bruteforce_checks_total", []string{"result", "allowed"}, 3},
		{"bruteforce_checks_total", []string{"result", "locked"}, 3},
		{"bruteforce_locks_total", []string{"scope", "pair"}, 1},
		{"bruteforce_locks_total", []string{"scope", "subject"}, 1},
		{"bruteforce_cleanup_deleted_total", nil, 1},
	} {
		if got := d.Get(t, want.name, want.labels...); got.Kind != metrictest.Counter || got.Value != want.value {
			t.Errorf("%v (%s), want counter delta %v", got, got.Kind, want.value)
		}
	}
}
