// Package outboxtest — фейк outboxkit.Publisher для тестов домена без NATS.
package outboxtest

import (
	"context"
	"slices"
	"sync"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
)

var _ outboxkit.Publisher = (*Recorder)(nil)

// Event — опубликованное сообщение: subject и payload как есть (по ссылке).
type Event struct {
	Subject string
	Payload any
}

// Recorder — in-memory outboxkit.Publisher: копит опубликованные события.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

// Publish сохраняет событие; ошибки не возвращает.
func (r *Recorder) Publish(_ context.Context, subject string, payload any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, Event{Subject: subject, Payload: payload})
	return nil
}

// All — копия всех событий в порядке публикации.
func (r *Recorder) All() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// Published — payload'ы событий по subject в порядке публикации.
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

// Last — payload последнего события по subject либо nil.
func (r *Recorder) Last(subject string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range slices.Backward(r.events) {
		if v.Subject == subject {
			return v.Payload
		}
	}
	return nil
}

// Count — число опубликованных событий.
func (r *Recorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// Reset очищает накопленные события.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}
