package testkit

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
)

// outcomeTB записывает исход хелпера и текст отказа и, как testing.T, обрывает
// горутину на Skip и Fatal; остальное уходит в тест под ним.
type outcomeTB struct {
	testing.TB
	outcome, msg string
}

func (tb *outcomeTB) Helper()                   {}
func (tb *outcomeTB) Skip(a ...any)             { tb.stop("skip", fmt.Sprint(a...)) }
func (tb *outcomeTB) Skipf(f string, a ...any)  { tb.stop("skip", fmt.Sprintf(f, a...)) }
func (tb *outcomeTB) SkipNow()                  { tb.stop("skip", "") }
func (tb *outcomeTB) Fatal(a ...any)            { tb.stop("fail", fmt.Sprint(a...)) }
func (tb *outcomeTB) Fatalf(f string, a ...any) { tb.stop("fail", fmt.Sprintf(f, a...)) }
func (tb *outcomeTB) FailNow()                  { tb.stop("fail", "") }

func (tb *outcomeTB) stop(outcome, msg string) {
	tb.outcome, tb.msg = outcome, msg
	runtime.Goexit()
}

// runOutcome зовёт fn с outcomeTB поверх t в своей горутине: Goexit обрывает только её.
func runOutcome(t testing.TB, fn func(testing.TB)) *outcomeTB {
	tb := &outcomeTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(tb)
	}()
	<-done
	return tb
}

// Без Docker вне -short integration-тест падает: пропуск делал зелёными и
// make check, и integration-гейты потребителей, не открыв ни одного контейнера.
func TestRequireDocker_UnavailableDockerFailsOutsideShort(t *testing.T) {
	probe := dockerHealth
	dockerHealth = func() error { return errors.New("docker probe failed") }
	t.Cleanup(func() { dockerHealth = probe })

	tb := runOutcome(t, RequireDocker)

	want := "fail"
	if testing.Short() {
		want = "skip"
	}
	if tb.outcome != want {
		t.Fatalf("RequireDocker without Docker (-short=%v): outcome %q, want %q", testing.Short(), tb.outcome, want)
	}
}
