package testkit

import (
	"errors"
	"runtime"
	"testing"
)

// outcomeTB записывает исход RequireDocker и, как testing.T, обрывает горутину
// на Skip и Fatal.
type outcomeTB struct {
	testing.TB
	outcome string
}

func (tb *outcomeTB) Helper()               {}
func (tb *outcomeTB) Skip(...any)           { tb.stop("skip") }
func (tb *outcomeTB) Skipf(string, ...any)  { tb.stop("skip") }
func (tb *outcomeTB) SkipNow()              { tb.stop("skip") }
func (tb *outcomeTB) Fatal(...any)          { tb.stop("fail") }
func (tb *outcomeTB) Fatalf(string, ...any) { tb.stop("fail") }
func (tb *outcomeTB) FailNow()              { tb.stop("fail") }

func (tb *outcomeTB) stop(outcome string) {
	tb.outcome = outcome
	runtime.Goexit()
}

// Без Docker вне -short integration-тест падает: пропуск делал зелёными и
// make check, и integration-гейты потребителей, не открыв ни одного контейнера.
func TestRequireDocker_UnavailableDockerFailsOutsideShort(t *testing.T) {
	probe := dockerHealth
	dockerHealth = func() error { return errors.New("docker probe failed") }
	t.Cleanup(func() { dockerHealth = probe })

	tb := &outcomeTB{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		RequireDocker(tb)
	}()
	<-done

	want := "fail"
	if testing.Short() {
		want = "skip"
	}
	if tb.outcome != want {
		t.Fatalf("RequireDocker without Docker (-short=%v): outcome %q, want %q", testing.Short(), tb.outcome, want)
	}
}
