package clockkit

import (
	"sync"
	"time"
)

// Fixed — застывшие часы: Now всегда отдаёт один и тот же момент. Таймер с
// d > 0 не сработает никогда — время не движется; сценарии «прошёл интервал»
// тестируются на Tickable.
type Fixed struct{ c *fakeClock }

// NewFixed — часы, застывшие на моменте t.
func NewFixed(t time.Time) *Fixed { return &Fixed{c: newFakeClock(t)} }

// Now возвращает момент, на котором часы застыли.
func (f *Fixed) Now() time.Time { return f.c.currentTime() }

// Since возвращает разницу между застывшим моментом и t (отрицательную, если t в будущем).
func (f *Fixed) Since(t time.Time) time.Duration { return f.c.currentTime().Sub(t) }

// After возвращает канал таймера на d; при d > 0 он не сработает никогда.
func (f *Fixed) After(d time.Duration) <-chan time.Time { return f.c.newTimer(d).C() }

// NewTimer возвращает таймер на d; при d > 0 он не сработает никогда.
func (f *Fixed) NewTimer(d time.Duration) Timer { return f.c.newTimer(d) }

// Tickable — часы, которые двигает тест: время стоит, пока не позовут Advance
// или Set, и они же будят таймеры с наступившим дедлайном — до возврата из
// вызова, без sleep на стороне теста.
type Tickable struct{ c *fakeClock }

// NewTickable — часы, стоящие на моменте start.
func NewTickable(start time.Time) *Tickable { return &Tickable{c: newFakeClock(start)} }

// Now возвращает текущий момент часов.
func (c *Tickable) Now() time.Time { return c.c.currentTime() }

// Since возвращает разницу между текущим моментом часов и t.
func (c *Tickable) Since(t time.Time) time.Duration { return c.c.currentTime().Sub(t) }

// After возвращает канал таймера на d от текущего момента часов.
func (c *Tickable) After(d time.Duration) <-chan time.Time { return c.c.newTimer(d).C() }

// NewTimer возвращает таймер на d от текущего момента часов.
func (c *Tickable) NewTimer(d time.Duration) Timer { return c.c.newTimer(d) }

// Advance двигает часы на d вперёд и будит все таймеры, чей дедлайн наступил.
func (c *Tickable) Advance(d time.Duration) { c.c.advance(d) }

// Set устанавливает момент t (в том числе в прошлое) и будит все таймеры, чей
// дедлайн наступил.
func (c *Tickable) Set(t time.Time) { c.c.set(t) }

// WaitTimers блокирует, пока на часах не окажется n ожидающих таймеров: снимает
// гонку между горутиной, которая ставит таймер, и тестом, который двигает часы.
// Сработавший таймер очередь покидает, поэтому после Advance тот же вызов ждёт
// уже следующий таймер, а не только что сработавший.
func (c *Tickable) WaitTimers(n int) { c.c.waitTimers(n) }

// fakeClock — общая машинерия фейков: текущий момент и очередь таймеров под
// одним мьютексом, поэтому срабатывание происходит синхронно внутри Advance/Set.
type fakeClock struct {
	mu     sync.Mutex
	added  sync.Cond
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock(start time.Time) *fakeClock {
	c := &fakeClock{now: start}
	c.added.L = &c.mu
	return c
}

func (c *fakeClock) currentTime() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.fireDueLocked()
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	c.fireDueLocked()
}

func (c *fakeClock) newTimer(d time.Duration) *fakeTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, ch: make(chan time.Time, 1), deadline: c.now.Add(d), active: true}
	c.timers = append(c.timers, t)
	c.added.Broadcast()
	c.fireDueLocked()
	return t
}

func (c *fakeClock) waitTimers(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.timers) < n {
		c.added.Wait()
	}
}

func (c *fakeClock) fireDueLocked() {
	kept := c.timers[:0]
	for _, t := range c.timers {
		if t.deadline.After(c.now) {
			kept = append(kept, t)
			continue
		}
		t.active = false
		// Буфер на один момент: срабатывание не теряется и не блокирует Advance.
		select {
		case t.ch <- c.now:
		default:
		}
	}
	c.timers = kept
}

func (c *fakeClock) removeLocked(t *fakeTimer) {
	for i, x := range c.timers {
		if x == t {
			c.timers = append(c.timers[:i], c.timers[i+1:]...)
			return
		}
	}
}

// fakeTimer — таймер фейковых часов; поля под мьютексом clock.
type fakeTimer struct {
	clock    *fakeClock
	ch       chan time.Time
	deadline time.Time
	active   bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	return t.stopLocked()
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := t.stopLocked()
	t.active = true
	t.deadline = t.clock.now.Add(d)
	t.clock.timers = append(t.clock.timers, t)
	t.clock.added.Broadcast()
	t.clock.fireDueLocked()
	return was
}

// stopLocked снимает таймер и забирает неполученный момент, как time.Timer с Go 1.23: прежнее
// срабатывание после Stop и Reset не придёт, а таймер с неполученным моментом — ещё активен.
func (t *fakeTimer) stopLocked() bool {
	was := t.active
	t.active = false
	t.clock.removeLocked(t)
	select {
	case <-t.ch:
		was = true
	default:
	}
	return was
}
