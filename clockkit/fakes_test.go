package clockkit_test

import (
	"sync"
	"testing"
	"time"

	"git.stit.tech/akadmin/golibinfra/clockkit"
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Advance(time.Millisecond)
		}()
	}
	wg.Wait()
	if got := c.Now().Sub(time.Unix(0, 0)); got != 100*time.Millisecond {
		t.Fatalf("expected 100ms total, got %v", got)
	}
}
