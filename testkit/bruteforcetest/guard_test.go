package bruteforcetest_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/testkit/bruteforcetest"
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
	st, err := bruteforcetest.Permissive{}.Check(t.Context(), "u1", "1.1.1.1")
	if err != nil || st != nil {
		t.Fatalf("Check = %v, %v, want nil, nil", st, err)
	}
	if st, err := (bruteforcetest.Permissive{}).RecordFailure(t.Context(), "u1", "1.1.1.1"); st != nil || err != nil {
		t.Fatalf("RecordFailure = %v, %v, want nil, nil", st, err)
	}
	if err := (bruteforcetest.Permissive{}).RecordSuccess(t.Context(), "u1", "1.1.1.1"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
}

func TestLocked_ReturnsState(t *testing.T) {
	t.Parallel()
	until := time.Now().Add(15 * time.Minute)
	g := bruteforcetest.Locked{Until: until}
	st, err := g.Check(t.Context(), "u1", "1.1.1.1")
	if err != nil || st == nil || !st.Until.Equal(until) {
		t.Fatalf("Check = %v, %v", st, err)
	}
	st, err = g.RecordFailure(t.Context(), "u1", "1.1.1.1")
	if err != nil || st == nil || !st.Until.Equal(until) {
		t.Fatalf("RecordFailure = %v, %v", st, err)
	}
	if err := g.RecordSuccess(t.Context(), "u1", "1.1.1.1"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
}

func TestRecorder_TracksSubjectAndIP(t *testing.T) {
	t.Parallel()
	r := &bruteforcetest.Recorder{}
	ctx := t.Context()
	if _, err := r.RecordFailure(ctx, "u1", "203.0.113.7"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if _, err := r.RecordFailure(ctx, "u2", "203.0.113.8"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if err := r.RecordSuccess(ctx, "u1", "203.0.113.7"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	if got := r.Failures(); len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Fatalf("Failures=%v", got)
	}
	if got := r.Successes(); len(got) != 1 || got[0] != "u1" {
		t.Fatalf("Successes=%v", got)
	}
	want := []bruteforcetest.Attempt{{Subject: "u1", IP: "203.0.113.7"}, {Subject: "u2", IP: "203.0.113.8"}}
	got := r.Attempts()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Attempts=%v, want %v", got, want)
	}
	if succ := r.SuccessAttempts(); len(succ) != 1 || succ[0] != want[0] {
		t.Fatalf("SuccessAttempts=%v", succ)
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

	guards := map[string]bruteforcekit.Guard{
		"Permissive": bruteforcetest.Permissive{},
		"Locked":     bruteforcetest.Locked{Until: time.Now().Add(time.Minute)},
		"Recorder":   &bruteforcetest.Recorder{},
	}
	keys := []struct{ subject, ip string }{
		{"", "203.0.113.7"},
		{"u1", ""},
		{"u1", "203.0.113.7:443"},
		{"u1", "not-an-ip"},
		{strings.Repeat("a", bruteforcekit.MaxSubjectLen+1), "203.0.113.7"},
	}
	for name, g := range guards {
		for _, k := range keys {
			if _, err := g.Check(t.Context(), k.subject, k.ip); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
				t.Errorf("%s.Check(%q, %q) = %v, want ErrInvalidKey", name, k.subject, k.ip, err)
			}
			if _, err := g.RecordFailure(t.Context(), k.subject, k.ip); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
				t.Errorf("%s.RecordFailure(%q, %q) = %v, want ErrInvalidKey", name, k.subject, k.ip, err)
			}
			if err := g.RecordSuccess(t.Context(), k.subject, k.ip); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
				t.Errorf("%s.RecordSuccess(%q, %q) = %v, want ErrInvalidKey", name, k.subject, k.ip, err)
			}
		}
	}

	// Невалидный вызов не попадает в историю Recorder.
	r := &bruteforcetest.Recorder{}
	_, _ = r.RecordFailure(t.Context(), "u1", "")
	_ = r.RecordSuccess(t.Context(), "u1", "")
	if len(r.Attempts()) != 0 || len(r.SuccessAttempts()) != 0 {
		t.Errorf("Attempts=%v SuccessAttempts=%v, want empty", r.Attempts(), r.SuccessAttempts())
	}
}

// IPv6 фейки не сворачивают: агрегация — свойство конфига, который есть только
// у Counting, поэтому полный адрес обязан проходить как есть.
func TestFakesAcceptCanonicalIPv6(t *testing.T) {
	t.Parallel()

	r := &bruteforcetest.Recorder{}
	if _, err := r.RecordFailure(t.Context(), "u1", "2001:db8::1"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if got := r.Attempts(); len(got) != 1 || got[0].IP != "2001:db8::1" {
		t.Fatalf("Attempts=%v, want the address as passed", got)
	}
}

func TestRecorder_CheckNeverLocks(t *testing.T) {
	t.Parallel()
	r := &bruteforcetest.Recorder{}
	for range 5 {
		if _, err := r.RecordFailure(t.Context(), "u1", "203.0.113.7"); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	st, err := r.Check(t.Context(), "u1", "203.0.113.7")
	if st != nil || err != nil {
		t.Fatalf("Check = %v, %v, want nil, nil even after failures", st, err)
	}
}

func countingConfig() bruteforcekit.Config {
	return bruteforcekit.Config{
		MaxFailures:      3,
		LockDuration:     15 * time.Minute,
		CleanupRetention: time.Hour,
		CleanupInterval:  5 * time.Minute,
		IPv6Prefix:       bruteforcekit.FullIPv6Prefix,
	}
}

func TestCounting_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	if _, err := bruteforcetest.NewCounting(bruteforcekit.Config{}, nil); !errors.Is(err, bruteforcekit.ErrConfig) {
		t.Fatalf("NewCounting(zero) = %v, want ErrConfig", err)
	}
}

func TestCounting_LocksAfterMaxFailures(t *testing.T) {
	t.Parallel()
	clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	cfg := countingConfig()
	g, err := bruteforcetest.NewCounting(cfg, clock)
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	ctx := t.Context()

	for i := range cfg.MaxFailures - 1 {
		st, err := g.RecordFailure(ctx, "u1", "203.0.113.7")
		if err != nil || st != nil {
			t.Fatalf("RecordFailure %d = %v, %v, want nil", i, st, err)
		}
	}
	st, err := g.RecordFailure(ctx, "u1", "203.0.113.7")
	if err != nil || st == nil {
		t.Fatalf("RecordFailure at threshold = %v, %v, want lock", st, err)
	}
	if st.FailedCount != cfg.MaxFailures {
		t.Fatalf("FailedCount = %d, want %d", st.FailedCount, cfg.MaxFailures)
	}
	if st.Until != clock.Now().Add(cfg.LockDuration) {
		t.Fatalf("Until = %v, want %v", st.Until, clock.Now().Add(cfg.LockDuration))
	}

	// Другая пара (subject, ip) не задета.
	if st, err := g.Check(ctx, "u1", "203.0.113.8"); err != nil || st != nil {
		t.Fatalf("other ip Check = %v, %v, want nil", st, err)
	}

	// Истечение блокировки начинает новое окно, как в pgstore.
	clock.Advance(cfg.LockDuration + time.Second)
	if st, err := g.Check(ctx, "u1", "203.0.113.7"); err != nil || st != nil {
		t.Fatalf("Check after expiry = %v, %v, want nil", st, err)
	}
	st, err = g.RecordFailure(ctx, "u1", "203.0.113.7")
	if err != nil || st != nil {
		t.Fatalf("first failure of new window = %v, %v, want nil", st, err)
	}

	if err := g.RecordSuccess(ctx, "u1", "203.0.113.7"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	if st, err := g.Check(ctx, "u1", "203.0.113.7"); err != nil || st != nil {
		t.Fatalf("Check after success = %v, %v, want nil", st, err)
	}
}

// Ключ сворачивается как в pgstore: регистр и пробелы subject'а и адреса одной
// /64 не дают обойти блокировку.
func TestCounting_NormalizesKeyLikePgstore(t *testing.T) {
	t.Parallel()
	cfg := countingConfig()
	cfg.IPv6Prefix = 64
	g, err := bruteforcetest.NewCounting(cfg, nil)
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	ctx := t.Context()
	for _, k := range [][2]string{{"User@Example.com", "2001:db8:0:1::1"}, {" user@example.com ", "2001:db8:0:1::2"}} {
		if _, err := g.RecordFailure(ctx, k[0], k[1]); err != nil {
			t.Fatalf("RecordFailure(%q, %q): %v", k[0], k[1], err)
		}
	}
	st, err := g.RecordFailure(ctx, "USER@EXAMPLE.COM", "2001:db8:0:1:ffff::9")
	if err != nil || st == nil {
		t.Fatalf("third failure of one normalized key = %v, %v, want lock", st, err)
	}
}

func TestCounting_ValidatesKey(t *testing.T) {
	t.Parallel()
	g, err := bruteforcetest.NewCounting(countingConfig(), nil)
	if err != nil {
		t.Fatalf("NewCounting: %v", err)
	}
	ctx := t.Context()
	if _, err := g.Check(ctx, "", "203.0.113.7"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("Check with empty subject = %v, want ErrInvalidKey", err)
	}
	if _, err := g.RecordFailure(ctx, "u1", "not-an-ip"); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("RecordFailure with bad ip = %v, want ErrInvalidKey", err)
	}
	if err := g.RecordSuccess(ctx, "u1", ""); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
		t.Fatalf("RecordSuccess with empty ip = %v, want ErrInvalidKey", err)
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

	if _, err := g.RecordFailure(ctx, "fresh", "203.0.113.7"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	for range cfg.MaxFailures {
		if _, err := g.RecordFailure(ctx, "locked", "203.0.113.8"); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}

	if err := g.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if st, err := g.Check(ctx, "locked", "203.0.113.8"); err != nil || st == nil {
		t.Fatalf("locked pair must survive Cleanup: %v, %v", st, err)
	}
	// Строка без блокировки удалена: следующая неудача начинает счёт заново.
	st, err := g.RecordFailure(ctx, "fresh", "203.0.113.7")
	if err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if st != nil {
		t.Fatalf("RecordFailure = %v, want nil", st)
	}
}
