// Package outboxtest — фейк outboxkit.Publisher для тестов домена без NATS и Postgres: хранит JSON
// события, как outbox, и только из транзакции фейка pgtest, которую откат выкидывает.
package outboxtest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"git.stit.tech/stit-core/golibinfra/internal/jsonb"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
)

var _ outboxkit.Publisher = (*Recorder)(nil)

// Event — опубликованное событие: subject и JSON payload, как его хранит outbox и получит
// подписчик (сравнение — через json.Unmarshal в тип события).
type Event struct {
	Subject string
	Payload json.RawMessage
}

// Recorder — in-memory outboxkit.Publisher: копит события, опубликованные в транзакции фейка.
type Recorder struct {
	mu     sync.Mutex
	events []*Event
}

// Publish сохраняет событие, как Bus пишет его в outbox: payload — json.Marshal (без JSON-формы —
// ошибка), то, что Postgres JSONB не хранит (`\u0000`, одинокий суррогат), — ошибка. Событие — только
// из транзакции фейка pgtest (Identity, Recorder) — её откат его выкидывает; вне её —
// outboxkit.ErrNoTx, и при метке pgkit.WithTestTx от самого теста: она прятала бы код под тестом,
// который транзакцию не открыл.
func (r *Recorder) Publish(ctx context.Context, subject string, payload any) error {
	if !pgtest.InTx(ctx) {
		return fmt.Errorf("%w: open it with the Transactor of the code under test (pgtest.Identity), not pgkit.WithTestTx", outboxkit.ErrNoTx)
	}
	js, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outboxtest: marshal payload: %w", err)
	}
	if problem := jsonb.Unstorable(js); problem != "" {
		return fmt.Errorf("outboxtest: payload %s: Postgres JSONB does not store it", problem)
	}
	e := &Event{Subject: subject, Payload: js}
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
	pgtest.OnRollback(ctx, func() { r.remove(e) })
	return nil
}

func (r *Recorder) remove(e *Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = slices.DeleteFunc(r.events, func(x *Event) bool { return x == e })
}

// All — копия всех событий в порядке публикации.
func (r *Recorder) All() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	for i, e := range r.events {
		out[i] = Event{Subject: e.Subject, Payload: slices.Clone(e.Payload)}
	}
	return out
}

// Published — JSON событий по subject в порядке публикации.
func (r *Recorder) Published(subject string) []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []json.RawMessage
	for _, e := range r.events {
		if e.Subject == subject {
			out = append(out, slices.Clone(e.Payload))
		}
	}
	return out
}

// Last — JSON последнего события по subject либо nil.
func (r *Recorder) Last(subject string) json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range slices.Backward(r.events) {
		if e.Subject == subject {
			return slices.Clone(e.Payload)
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
