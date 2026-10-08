package clockkit_test

import (
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

var (
	_ clockkit.Clock = clockkit.Real{}
	_ clockkit.Clock = (*clockkit.Fixed)(nil)
	_ clockkit.Clock = (*clockkit.Tickable)(nil)
)

func TestFixed_AlwaysSameNow(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := clockkit.NewFixed(want)
	for range 3 {
		if got := c.Now(); !got.Equal(want) {
			t.Fatalf("Now=%v want %v", got, want)
		}
	}
	if got := c.Since(want.Add(-time.Hour)); got != time.Hour {
		t.Fatalf("Since=%v want 1h", got)
	}
}

// Застывшие часы не двигаются, поэтому таймер с d > 0 не срабатывает, а с
// d <= 0 срабатывает сразу — как time.NewTimer(0).
func TestFixed_Timers(t *testing.T) {
	t.Parallel()
	c := clockkit.NewFixed(time.Unix(0, 0))

	pending := c.NewTimer(time.Hour)
	select {
	case v := <-pending.C():
		t.Fatalf("timer on a frozen clock fired: %v", v)
	default:
	}
	if !pending.Stop() {
		t.Fatal("Stop on a pending timer must report it was active")
	}

	select {
	case <-c.After(0):
	default:
		t.Fatal("After(0) must fire immediately")
	}
}

func TestTickable_AdvanceAndSet(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := clockkit.NewTickable(start)
	if !c.Now().Equal(start) {
		t.Fatalf("initial Now=%v want %v", c.Now(), start)
	}
	c.Advance(time.Hour)
	if !c.Now().Equal(start.Add(time.Hour)) {
		t.Fatalf("after Advance Now=%v", c.Now())
	}
	if got := c.Since(start); got != time.Hour {
		t.Fatalf("Since=%v want 1h", got)
	}
	c.Set(start)
	if !c.Now().Equal(start) {
		t.Fatalf("after Set Now=%v", c.Now())
	}
}

func TestTickable_ConcurrentAdvanceIsSafe(t *testing.T) {
	t.Parallel()
	c := clockkit.NewTickable(time.Unix(0, 0))
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			c.Advance(time.Millisecond)
		})
	}
	wg.Wait()
	if got := c.Now().Sub(time.Unix(0, 0)); got != 100*time.Millisecond {
		t.Fatalf("expected 100ms total, got %v", got)
	}
}

// Срабатывание происходит внутри Advance, поэтому «ещё не сработал» проверяется
// без sleep: канал пуст сразу после недостаточного сдвига.
func TestTickable_TimerFiresOnAdvance(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_700_000_000, 0)
	c := clockkit.NewTickable(start)
	ch := c.After(10 * time.Second)

	c.Advance(9 * time.Second)
	select {
	case v := <-ch:
		t.Fatalf("timer fired after 9s of 10s: %v", v)
	default:
	}

	c.Advance(time.Second)
	select {
	case v := <-ch:
		if !v.Equal(start.Add(10 * time.Second)) {
			t.Fatalf("fired at %v, want %v", v, start.Add(10*time.Second))
		}
	default:
		t.Fatal("timer did not fire at its deadline")
	}
}

// Set в прошлое не будит таймер, Set за дедлайн — будит.
func TestTickable_SetFiresDueTimers(t *testing.T) {
	t.Parallel()
	start := time.Unix(0, 0)
	c := clockkit.NewTickable(start)
	ch := c.After(time.Minute)

	c.Set(start.Add(-time.Hour))
	select {
	case <-ch:
		t.Fatal("timer fired after moving the clock backwards")
	default:
	}

	c.Set(start.Add(time.Hour))
	select {
	case <-ch:
	default:
		t.Fatal("timer did not fire after the clock passed its deadline")
	}
}

func TestTickable_TimerStopAndReset(t *testing.T) {
	t.Parallel()
	c := clockkit.NewTickable(time.Unix(0, 0))

	stopped := c.NewTimer(time.Minute)
	if !stopped.Stop() {
		t.Fatal("Stop on a pending timer must report it was active")
	}
	if stopped.Stop() {
		t.Fatal("second Stop must report the timer was not active")
	}
	c.Advance(time.Hour)
	select {
	case <-stopped.C():
		t.Fatal("stopped timer must not fire")
	default:
	}

	if stopped.Reset(time.Minute) {
		t.Fatal("Reset of a stopped timer must report it was not active")
	}
	c.Advance(time.Minute)
	select {
	case <-stopped.C():
	default:
		t.Fatal("timer did not fire after Reset")
	}

	live := c.NewTimer(time.Minute)
	if !live.Reset(2 * time.Minute) {
		t.Fatal("Reset of a pending timer must report it was active")
	}
	c.Advance(time.Minute)
	select {
	case <-live.C():
		t.Fatal("timer fired at the old deadline after Reset")
	default:
	}
	c.Advance(time.Minute)
	select {
	case <-live.C():
	default:
		t.Fatal("timer did not fire at the new deadline")
	}
}

// Таймер фейка — time.Timer Go 1.23, те же сценарии идут по Real: момент сработавшего таймера,
// который ещё не получили, Stop и Reset забирают и считают таймер активным; полученный — false.
func TestTimer_StopAndResetLikeTimeTimer(t *testing.T) {
	t.Parallel()
	const d = 5 * time.Millisecond
	tickable := clockkit.NewTickable(time.Unix(0, 0))
	clocks := map[string]struct {
		clock clockkit.Clock
		pass  func() // дедлайн таймера на d наступает
	}{
		"real":     {clockkit.Real{}, func() { time.Sleep(4 * d) }},
		"tickable": {tickable, func() { tickable.Advance(d) }},
	}
	for name, c := range clocks {
		t.Run(name, func(t *testing.T) {
			idle := func(timer clockkit.Timer, when string) {
				t.Helper()
				select {
				case v := <-timer.C():
					t.Fatalf("%s: stale moment %v", when, v)
				default:
				}
			}
			fired := func(timer clockkit.Timer, when string) {
				t.Helper()
				select {
				case <-timer.C():
				case <-time.After(5 * time.Second):
					t.Fatalf("%s: the timer did not fire", when)
				}
			}

			stopped := c.clock.NewTimer(d)
			c.pass()
			if !stopped.Stop() {
				t.Fatal("Stop of a fired timer nobody received from must report it was active")
			}
			idle(stopped, "after Stop")
			if stopped.Stop() {
				t.Fatal("second Stop must report the timer was not active")
			}

			reset := c.clock.NewTimer(d)
			c.pass()
			if !reset.Reset(time.Hour) {
				t.Fatal("Reset of a fired timer nobody received from must report it was active")
			}
			idle(reset, "after Reset")
			if !reset.Reset(d) {
				t.Fatal("Reset of a pending timer must report it was active")
			}
			c.pass()
			fired(reset, "after the second Reset")

			received := c.clock.NewTimer(d)
			c.pass()
			fired(received, "at the deadline")
			if received.Stop() {
				t.Fatal("Stop after the moment was received must report the timer was not active")
			}
			if received.Reset(d) {
				t.Fatal("Reset after the moment was received must report the timer was not active")
			}
			c.pass()
			fired(received, "after Reset of a received timer")
		})
	}
}

// WaitTimers снимает гонку «горутина ещё не поставила таймер, а тест уже двигает
// часы»: без него Advance прошёл бы мимо и тест повис на канале.
func TestTickable_WaitTimersSynchronisesWithGoroutine(t *testing.T) {
	t.Parallel()
	c := clockkit.NewTickable(time.Unix(0, 0))
	woke := make(chan time.Time, 1)
	go func() { woke <- <-c.After(time.Hour) }()

	c.WaitTimers(1)
	c.Advance(time.Hour)

	select {
	case got := <-woke:
		if !got.Equal(time.Unix(0, 0).Add(time.Hour)) {
			t.Fatalf("woke at %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine was not woken by Advance")
	}
}

func TestTickable_ConcurrentTimersAndAdvance(t *testing.T) {
	t.Parallel()
	c := clockkit.NewTickable(time.Unix(0, 0))
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			timer := c.NewTimer(time.Millisecond)
			timer.Stop()
			timer.Reset(time.Millisecond)
			<-timer.C()
		})
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	for {
		select {
		case <-done:
			return
		default:
			c.Advance(time.Millisecond)
		}
	}
}
