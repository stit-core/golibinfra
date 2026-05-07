package eventbus

import (
	"context"
	"sync"
)

type Event struct {
	Subject string
	Payload any
}

type Recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *Recorder) Publish(_ context.Context, subject string, payload any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, Event{Subject: subject, Payload: payload})
	return nil
}

func (r *Recorder) All() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

func (r *Recorder) Published(subject string) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []any
	for _, e := range r.events {
		if e.Subject == subject {
			out = append(out, e.Payload)
		}
	}
	return out
}

func (r *Recorder) Last(subject string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Subject == subject {
			return r.events[i].Payload
		}
	}
	return nil
}

func (r *Recorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}
