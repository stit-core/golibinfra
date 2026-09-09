package clockkit_test

import (
	"testing"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

func TestReal_NowAndSince(t *testing.T) {
	t.Parallel()
	c := clockkit.Real{}
	before := time.Now()
	got := c.Now()
	if got.Before(before) || got.After(time.Now()) {
		t.Fatalf("Now=%v outside [%v, now]", got, before)
	}
	if d := c.Since(got); d < 0 {
		t.Fatalf("Since=%v, want >= 0", d)
	}
}

func TestReal_Timers(t *testing.T) {
	t.Parallel()
	c := clockkit.Real{}

	select {
	case <-c.After(time.Millisecond):
	case <-time.After(5 * time.Second):
		t.Fatal("After did not fire")
	}

	timer := c.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("Stop on a pending timer must report it was active")
	}
	if timer.Reset(time.Millisecond) {
		t.Fatal("Reset of a stopped timer must report it was not active")
	}
	select {
	case <-timer.C():
	case <-time.After(5 * time.Second):
		t.Fatal("timer did not fire after Reset")
	}
}

func TestModule_ProvidesReal(t *testing.T) {
	t.Parallel()
	var got clockkit.Clock
	app := fx.New(
		fx.NopLogger,
		clockkit.Module,
		fx.Invoke(func(c clockkit.Clock) { got = c }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	if _, ok := got.(clockkit.Real); !ok {
		t.Fatalf("Module provided %T, want clockkit.Real", got)
	}
}
