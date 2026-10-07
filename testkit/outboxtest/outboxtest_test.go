package outboxtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit/outboxtest"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
)

type narrowBus interface {
	Publish(ctx context.Context, subject string, payload any) error
}

var _ narrowBus = (*outboxtest.Recorder)(nil)

// inTx публикует в транзакции фейка, как код под тестом через свой Transactor.
func inTx(t *testing.T, r *outboxtest.Recorder, subject string, payload any) error {
	t.Helper()
	return pgtest.Identity{}.WithTx(t.Context(), func(ctx context.Context) error { return r.Publish(ctx, subject, payload) })
}

func TestRecorder_PublishAndRead(t *testing.T) {
	t.Parallel()
	r := &outboxtest.Recorder{}
	for _, e := range []struct {
		subject string
		payload any
	}{{"user.created", "u1"}, {"user.created", "u2"}, {"auth.login", map[string]int{"n": 1}}} {
		if err := inTx(t, r, e.subject, e.payload); err != nil {
			t.Fatal(err)
		}
	}

	if r.Count() != 3 {
		t.Fatalf("Count=%d want 3", r.Count())
	}
	got := r.Published("user.created")
	if len(got) != 2 || string(got[0]) != `"u1"` || string(got[1]) != `"u2"` {
		t.Fatalf("Published(user.created)=%s", got)
	}
	if string(r.Last("auth.login")) != `{"n":1}` {
		t.Fatalf("Last(auth.login)=%s", r.Last("auth.login"))
	}
	if r.Last("missing") != nil {
		t.Fatalf("Last(missing) must be nil")
	}
	r.Reset()
	if r.Count() != 0 {
		t.Fatalf("Reset failed")
	}
}

// Payload — снимок JSON на момент Publish: правка значения caller'ом и копий, отданных Recorder'ом,
// записанное событие не меняет.
func TestRecorder_StoresJSONSnapshot(t *testing.T) {
	t.Parallel()
	r := &outboxtest.Recorder{}
	payload := map[string]string{"id": "1"}
	if err := inTx(t, r, "a", payload); err != nil {
		t.Fatal(err)
	}
	payload["id"] = "tampered"
	all := r.All()
	all[0].Subject, all[0].Payload[0] = "tampered", 'X'
	r.Last("a")[0] = 'X'
	if got := r.All(); len(got) != 1 || got[0].Subject != "a" || string(got[0].Payload) != `{"id":"1"}` {
		t.Fatalf("All() = %+v, want the event as published", got)
	}
}

// Payload, который Bus не смаршалит или Postgres JSONB не сохранит, — ошибка, а не событие.
func TestRecorder_RejectsWhatTheOutboxRejects(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string]any{
		"chan":            map[string]any{"c": make(chan int)},
		"NaN":             map[string]float64{"r": math.NaN()},
		"NUL in a string": map[string]string{"s": "a\x00b"},
		"lone surrogate":  map[string]any{"raw": json.RawMessage(`"\ud800"`)},
	} {
		r := &outboxtest.Recorder{}
		if err := inTx(t, r, "x", payload); err == nil || r.Count() != 0 {
			t.Errorf("%s: err = %v, Count = %d, want an error and no event", name, err, r.Count())
		}
	}
}

// Регрессия D02/M34 аудита шаблона и бэклога outboxtest-withtesttx: usecase, публикующий мимо
// своего Transactor, краснеет в тесте домена — и вне транзакции, и под меткой pgkit.WithTestTx от
// самого теста.
func TestRecorder_PublishRequiresFakeTx(t *testing.T) {
	t.Parallel()
	r := &outboxtest.Recorder{}
	for name, ctx := range map[string]context.Context{
		"no tx":            t.Context(),
		"pgkit.WithTestTx": pgkit.WithTestTx(t.Context()),
	} {
		if err := r.Publish(ctx, "note.created", "n1"); !errors.Is(err, outboxkit.ErrNoTx) {
			t.Fatalf("%s: err = %v, want ErrNoTx", name, err)
		}
	}
	if r.Count() != 0 {
		t.Fatalf("event recorded outside the fake tx: Count = %d, want 0", r.Count())
	}
	if err := inTx(t, r, "note.created", "n1"); err != nil {
		t.Fatalf("Publish in tx: %v", err)
	}
	if string(r.Last("note.created")) != `"n1"` {
		t.Fatalf("Last(note.created) = %s, want \"n1\"", r.Last("note.created"))
	}
}

// Ошибка fn транзакции выкидывает её события, как откат — строки outbox; события других
// транзакций остаются.
func TestRecorder_RollbackDropsEvents(t *testing.T) {
	t.Parallel()
	r := &outboxtest.Recorder{}
	if err := inTx(t, r, "kept", 1); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := pgtest.Identity{}.WithTx(t.Context(), func(ctx context.Context) error {
		if err := r.Publish(ctx, "dropped", 2); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || r.Count() != 1 || r.Last("dropped") != nil || r.Last("kept") == nil {
		t.Fatalf("err = %v, events = %+v, want only the committed event", err, r.All())
	}
}
