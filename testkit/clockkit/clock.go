package clockkit

import (
	"sync"
	"time"
)

type Fixed struct {
	t time.Time
}

func NewFixed(t time.Time) *Fixed { return &Fixed{t: t} }

func (f *Fixed) Now() time.Time { return f.t }

type Tickable struct {
	mu  sync.Mutex
	now time.Time
}

func NewTickable(start time.Time) *Tickable { return &Tickable{now: start} }

func (c *Tickable) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Tickable) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *Tickable) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}
