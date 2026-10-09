package bruteforcetest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit/bruteforcetest"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
)

var (
	_ bruteforcekit.Guard   = bruteforcetest.Permissive{}
	_ bruteforcekit.Guard   = bruteforcetest.Locked{}
	_ bruteforcekit.Guard   = (*bruteforcetest.Recorder)(nil)
	_ bruteforcekit.Guard   = (*bruteforcetest.Counting)(nil)
	_ bruteforcekit.Cleaner = (*bruteforcetest.Counting)(nil)
)

func TestPermissive_NoLock(t *testing.T) {
	t.Parallel()
	for range 10 {
		if st, err := (bruteforcetest.Permissive{}).Attempt(t.Context(), "u1", "1.1.1.1"); err != nil || st != nil {
			t.Fatalf("Attempt = %v, %v, want nil, nil", st, err)
		}
	}
	if err := (bruteforcetest.Permissive{}).RecordSuccess(t.Context(), "u1", "1.1.1.1"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
}

func TestLocked_ReturnsState(t *testing.T) {
	t.Parallel()
	until := time.Now().Add(15 * time.Minute)
	g := bruteforcetest.Locked{Until: until}
	st, err := g.Attempt(t.Context(), "u1", "1.1.1.1")
	if err != nil || st == nil || !st.Until.Equal(until) {
		t.Fatalf("Attempt = %v, %v", st, err)
	}
	if err := g.RecordSuccess(t.Context(), "u1", "1.1.1.1"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
}

func TestRecorder_TracksSubjectAndIP(t *testing.T) {
	t.Parallel()
	r := &bruteforcetest.Recorder{}
	ctx := t.Context()
	for _, k := range []bruteforcetest.Attempt{{Subject: "u1", IP: "203.0.113.7"}, {Subject: "u2", IP: "203.0.113.8"}} {
		if st, err := r.Attempt(ctx, k.Subject, k.IP); err != nil || st != nil {
			t.Fatalf("Attempt = %v, %v, want allowed", st, err)
		}
	}
	if err := r.RecordSuccess(ctx, "u1", "203.0.113.7"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	want := []bruteforcetest.Attempt{{Subject: "u1", IP: "203.0.113.7"}, {Subject: "u2", IP: "203.0.113.8"}}
	got := r.Attempts()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Attempts=%v, want %v", got, want)
	}
	if succ := r.Successes(); len(succ) != 1 || succ[0] != want[0] {
		t.Fatalf("Successes=%v", succ)
	}

	got[0].IP = "tampered"
	if r.Attempts()[0].IP != "203.0.113.7" {
		t.Fatal("Attempts must return a copy")
	}
}

// Регрессия: фейк без проверки ключа пропускал бы проводку, которая отдаёт
// guard'у пустой ip, — в проде это ErrInvalidKey и 500 на логине.
func TestFakesRejectInvalidKeyLikePgstore(t *testing.T) {
	t.Parallel()

	counting, err := bruteforcetest.NewCounting(countingConfig(), nil)
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	guards := map[string]bruteforcekit.Guard{
		"Permissive": bruteforcetest.Permissive{},
		"Locked":     bruteforcetest.Locked{Until: time.Now().Add(time.Minute)},
		"Recorder":   &bruteforcetest.Recorder{},
		"Counting":   counting,
	}
	keys := []struct{ subject, ip string }{
		{"", "203.0.113.7"},
		{"  ", "203.0.113.7"},
		{"u1", ""},
		{"u1", "203.0.113.7:443"},
		{"u1", "not-an-ip"},
		{"u1\x00", "203.0.113.7"},
		{"u\xff1", "203.0.113.7"},
	}
	for name, g := range guards {
		for _, k := range keys {
			if _, err := g.Attempt(t.Context(), k.subject, k.ip); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
				t.Errorf("%s.Attempt(%q, %q) = %v, want ErrInvalidKey", name, k.subject, k.ip, err)
			}
			if err := g.RecordSuccess(t.Context(), k.subject, k.ip); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
				t.Errorf("%s.RecordSuccess(%q, %q) = %v, want ErrInvalidKey", name, k.subject, k.ip, err)
			}
		}
	}

	// Невалидный вызов не попадает в историю Recorder.
	r := &bruteforcetest.Recorder{}
	_, _ = r.Attempt(t.Context(), "u1", "")
	_ = r.RecordSuccess(t.Context(), "u1", "")
	if len(r.Attempts()) != 0 || len(r.Successes()) != 0 {
		t.Errorf("Attempts=%v Successes=%v, want empty", r.Attempts(), r.Successes())
	}
}

// Как pgstore, фейки отвергают Attempt внутри транзакции: сервис, учитывающий
// попытку в Transactor.WithTx, краснеет в юнит-тесте с фейком pgtest — в проде
// откат на неверном пароле снимал бы учёт. RecordSuccess в транзакции допустим.
func TestFakesRejectAttemptInTransaction(t *testing.T) {
	t.Parallel()

	counting, err := bruteforcetest.NewCounting(countingConfig(), nil)
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	recorder := &bruteforcetest.Recorder{}
	guards := map[string]bruteforcekit.Guard{
		"Permissive": bruteforcetest.Permissive{},
		"Locked":     bruteforcetest.Locked{Until: time.Now().Add(time.Minute)},
		"Recorder":   recorder,
		"Counting":   counting,
	}
	inTx := pgkit.WithTestTx(t.Context())
	for name, g := range guards {
		if st, err := g.Attempt(inTx, "u1", "203.0.113.7"); !errors.Is(err, bruteforcekit.ErrInTx) || st != nil {
			t.Errorf("%s.Attempt in a transaction = %v, %v, want ErrInTx", name, st, err)
		}
		if err := g.RecordSuccess(inTx, "u1", "203.0.113.7"); err != nil {
			t.Errorf("%s.RecordSuccess in a transaction: %v", name, err)
		}
	}
	if got := recorder.Attempts(); len(got) != 0 {
		t.Errorf("Attempts=%v, want the rejected call unrecorded", got)
	}
}

// Ключ Recorder хранит как передан: агрегация — свойство конфига, который есть
// только у Counting, и тест сервиса смотрит, что передал сервис.
func TestRecorder_KeepsKeyAsPassed(t *testing.T) {
	t.Parallel()

	r := &bruteforcetest.Recorder{}
	if _, err := r.Attempt(t.Context(), "User@Example.com", "2001:db8::1"); err != nil {
		t.Fatalf("Attempt: %v", err)
	}
	if got := r.Attempts(); len(got) != 1 || got[0] != (bruteforcetest.Attempt{Subject: "User@Example.com", IP: "2001:db8::1"}) {
		t.Fatalf("Attempts=%v, want the key as passed", got)
	}
}

func countingConfig() bruteforcekit.Config {
	return bruteforcekit.Config{
		MaxFailures:         3,
		LockDuration:        15 * time.Minute,
		SubjectMaxFailures:  bruteforcekit.SubjectBudgetOff,
		SubjectLockDuration: time.Minute,
		CleanupRetention:    time.Hour,
		CleanupInterval:     5 * time.Minute,
		IPv6Prefix:          bruteforcekit.FullIPv6Prefix,
	}
}

func TestCounting_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	if _, err := bruteforcetest.NewCounting(bruteforcekit.Config{}, nil); !errors.Is(err, bruteforcekit.ErrConfig) {
		t.Fatalf("NewCounting(zero) = %v, want ErrConfig", err)
	}
}

func TestCounting_LocksAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	cfg := countingConfig()
	g, err := bruteforcetest.NewCounting(cfg, clock)
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	ctx := t.Context()

	for i := range cfg.MaxFailures {
		if st, err := g.Attempt(ctx, "u1", "203.0.113.7"); err != nil || st != nil {
			t.Fatalf("Attempt %d = %v, %v, want allowed", i+1, st, err)
		}
	}
	st, err := g.Attempt(ctx, "u1", "203.0.113.7")
	if err != nil || st == nil {
		t.Fatalf("Attempt over budget = %v, %v, want lock", st, err)
	}
	if st.FailedCount != cfg.MaxFailures {
		t.Fatalf("FailedCount = %d, want %d", st.FailedCount, cfg.MaxFailures)
	}
	if want := clock.Now().Add(cfg.LockDuration); !st.Until.Equal(want) {
		t.Fatalf("Until = %v, want %v", st.Until, want)
	}

	// Попытка под блокировкой её не продлевает и счётчик не меняет.
	clock.Advance(time.Minute)
	again, err := g.Attempt(ctx, "u1", "203.0.113.7")
	if err != nil || again == nil || !again.Until.Equal(st.Until) || again.FailedCount != st.FailedCount {
		t.Fatalf("Attempt under lock = %+v, %v, want the same lock %+v", again, err, st)
	}

	// Другая пара (subject, ip) не задета.
	if st, err := g.Attempt(ctx, "u1", "203.0.113.8"); err != nil || st != nil {
		t.Fatalf("other ip Attempt = %v, %v, want allowed", st, err)
	}

	// Истечение блокировки начинает новое окно, как в pgstore.
	clock.Advance(cfg.LockDuration)
	for i := range cfg.MaxFailures {
		if st, err := g.Attempt(ctx, "u1", "203.0.113.7"); err != nil || st != nil {
			t.Fatalf("new window Attempt %d = %v, %v, want allowed", i+1, st, err)
		}
	}

	// Успех снимает и блокировку, выставленную последней попыткой окна.
	if err := g.RecordSuccess(ctx, "u1", "203.0.113.7"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	if st, err := g.Attempt(ctx, "u1", "203.0.113.7"); err != nil || st != nil {
		t.Fatalf("Attempt after success = %v, %v, want allowed", st, err)
	}
}

// Ключ сворачивается как в pgstore: регистр и пробелы subject'а и адреса одной
// /64 не дают обойти блокировку.
func TestCounting_NormalizesKeyLikePgstore(t *testing.T) {
	t.Parallel()
	cfg := countingConfig()
	cfg.MaxFailures = 2
	cfg.IPv6Prefix = 64
	g, err := bruteforcetest.NewCounting(cfg, nil)
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	ctx := t.Context()
	for _, k := range [][2]string{{"User@Example.com", "2001:db8:0:1::1"}, {" user@example.com ", "2001:db8:0:1::2"}} {
		if st, err := g.Attempt(ctx, k[0], k[1]); err != nil || st != nil {
			t.Fatalf("Attempt(%q, %q) = %v, %v, want allowed", k[0], k[1], st, err)
		}
	}
	st, err := g.Attempt(ctx, "USER@EXAMPLE.COM", "2001:db8:0:1:ffff::9")
	if err != nil || st == nil {
		t.Fatalf("third attempt of one normalized key = %v, %v, want lock", st, err)
	}
}

func TestCounting_CleanupKeepsActiveLocks(t *testing.T) {
	t.Parallel()
	clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	cfg := countingConfig()
	g, err := bruteforcetest.NewCounting(cfg, clock)
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	ctx := t.Context()

	if _, err := g.Attempt(ctx, "fresh", "203.0.113.7"); err != nil {
		t.Fatalf("Attempt: %v", err)
	}
	for range cfg.MaxFailures {
		if _, err := g.Attempt(ctx, "locked", "203.0.113.8"); err != nil {
			t.Fatalf("Attempt: %v", err)
		}
	}

	if err := g.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if st, err := g.Attempt(ctx, "locked", "203.0.113.8"); err != nil || st == nil {
		t.Fatalf("locked pair must survive Cleanup: %v, %v", st, err)
	}
	// Строка без блокировки удалена: счёт начинается заново, окно снова целое.
	for i := range cfg.MaxFailures {
		if st, err := g.Attempt(ctx, "fresh", "203.0.113.7"); err != nil || st != nil {
			t.Fatalf("Attempt %d after Cleanup = %v, %v, want allowed", i+1, st, err)
		}
	}
}

// Бюджет субъекта — как в pgstore: SubjectMaxFailures попыток со всех адресов, затем
// короткая блокировка субъекта с RetryAfter по часам фейка; под ней пара не учитывается,
// истечение начинает новое окно, успех снимает его.
func TestCounting_SubjectBudget(t *testing.T) {
	t.Parallel()
	clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	cfg := countingConfig()
	cfg.SubjectMaxFailures = 4
	g, err := bruteforcetest.NewCounting(cfg, clock)
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	ctx := t.Context()

	allowed := 0
	var last *bruteforcekit.LockState
	for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
		for range cfg.MaxFailures {
			st, err := g.Attempt(ctx, "u1", ip)
			if err != nil {
				t.Fatalf("Attempt: %v", err)
			}
			if st == nil {
				allowed++
			} else {
				last = st
			}
		}
	}
	if allowed != cfg.SubjectMaxFailures || last == nil || last.Scope != bruteforcekit.ScopeSubject || last.RetryAfter != cfg.SubjectLockDuration {
		t.Fatalf("allowed %d, last lock %+v; want %d and a subject lock for %s", allowed, last, cfg.SubjectMaxFailures, cfg.SubjectLockDuration)
	}

	clock.Advance(cfg.SubjectLockDuration)
	if st, err := g.Attempt(ctx, "u1", "203.0.113.9"); err != nil || st != nil {
		t.Fatalf("Attempt after the subject lock = %+v, %v, want allowed", st, err)
	}
	for range cfg.SubjectMaxFailures - 2 {
		_, _ = g.Attempt(ctx, "u1", "203.0.113.10")
	}
	if err := g.RecordSuccess(ctx, "u1", "203.0.113.10"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	for i := range cfg.SubjectMaxFailures {
		if st, err := g.Attempt(ctx, "u1", fmt.Sprintf("203.0.113.%d", 20+i)); err != nil || st != nil {
			t.Fatalf("Attempt %d after success = %+v, %v, want the subject window reset", i+1, st, err)
		}
	}
}

// Locked отдаёт блокировку пары с остатком до Until.
func TestLocked_RetryAfter(t *testing.T) {
	t.Parallel()
	st, err := bruteforcetest.Locked{Until: time.Now().Add(time.Hour)}.Attempt(t.Context(), "u1", "203.0.113.7")
	if err != nil || st == nil || st.Scope != bruteforcekit.ScopePair || st.RetryAfter <= 59*time.Minute {
		t.Fatalf("Attempt = %+v, %v; want a pair lock with about an hour left", st, err)
	}
}

// Регрессия RTST-03: RecordSuccess в транзакции, которая откатилась, счётчики не стирает — как
// у pgstore (так обещает контракт Guard): вход, не дошедший до коммита, попытки не обнуляет.
func TestCounting_RecordSuccessFollowsTheTransaction(t *testing.T) {
	t.Parallel()
	cfg := countingConfig()
	g, err := bruteforcetest.NewCounting(cfg, clockkit.NewTickable(time.Unix(1_700_000_000, 0)))
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	ctx := t.Context()
	attempt := func() *bruteforcekit.LockState {
		t.Helper()
		st, err := g.Attempt(ctx, "u1", "203.0.113.7")
		if err != nil {
			t.Fatalf("Attempt: %v", err)
		}
		return st
	}
	for range cfg.MaxFailures - 1 {
		attempt()
	}
	failed := errors.New("login failed after the guard")
	if err := (pgtest.Identity{}).WithTx(ctx, func(ctx context.Context) error {
		if err := g.RecordSuccess(ctx, "u1", "203.0.113.7"); err != nil {
			return err
		}
		return failed
	}); !errors.Is(err, failed) {
		t.Fatalf("WithTx = %v, want the error of fn", err)
	}
	attempt()
	if st := attempt(); st == nil {
		t.Fatal("the rolled back RecordSuccess erased the attempts: the pair is not locked after MaxFailures")
	}

	if err := (pgtest.Identity{}).WithTx(ctx, func(ctx context.Context) error { return g.RecordSuccess(ctx, "u1", "203.0.113.7") }); err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if st := attempt(); st != nil {
		t.Fatalf("the committed RecordSuccess must erase the lock, got %+v", st)
	}
}
