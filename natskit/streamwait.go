package natskit

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"
)

const (
	// streamWaitFirst и streamWaitMax — отступ повторов подписки на поток, которого нет: удваивается до потолка, и
	// подписка встаёт не позже чем через streamWaitMax после появления потока.
	streamWaitFirst = time.Second
	streamWaitMax   = 30 * time.Second
	// streamWaitRemind — как часто ожидание повторяет WARN: на дефолтах отступа запись на каждую попытку была бы
	// шумом, а одна на старте терялась бы в логе.
	streamWaitRemind = 5 * time.Minute
	// streamWaitAttempt — потолок одной попытки завести консьюмер: на переподключении запрос к JetStream API
	// иначе висел бы до своего таймаута.
	streamWaitAttempt = 5 * time.Second
)

// streamWaitPolicy — отступ и напоминание ожидания потока; поля задают тесты.
type streamWaitPolicy struct {
	first, max, remind time.Duration
}

func defaultStreamWait() streamWaitPolicy {
	return streamWaitPolicy{first: streamWaitFirst, max: streamWaitMax, remind: streamWaitRemind}
}

// awaitStream — подписка на поток, которого ещё нет: handle отдаётся сразу, консьюмер заводит фоновая попытка, когда
// поток появится. Unsubscribe, Drain и Close шины снимают ожидание.
func (b *jsBus) awaitStream(sc streamConsumer, handler Handler, release func()) (*subscription, error) {
	return b.c.subscribe(func() (*subscription, error) {
		s := newSubscription(b.c, sc.filter)
		s.onForget = release
		w := &streamWait{b: b, s: s, sc: sc, handler: handler, done: make(chan struct{})}
		w.ctx, w.cancel = context.WithCancel(context.Background())
		s.done = w.done
		s.stop = w.stop
		go w.run()
		return s, nil
	})
}

// streamWait — ожидание потока одной подписки: попытки завести консьюмер с растущим отступом, затем доставка.
type streamWait struct {
	b       *jsBus
	s       *subscription
	sc      streamConsumer
	handler Handler
	// ctx отменяет stop: прерывает ожидание и идущую попытку.
	ctx    context.Context
	cancel context.CancelFunc
	// done закрывается, когда ожидание снято или доставка консьюмера закрыта.
	done chan struct{}

	mu      sync.Mutex
	stopped bool
	cc      jetstream.ConsumeContext
}

func (w *streamWait) stop(drain bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	w.cancel()
	if w.cc != nil {
		stopConsume(w.cc, drain)
	}
	return nil
}

func (w *streamWait) run() {
	defer close(w.done)
	if cc := w.await(); cc != nil {
		<-cc.Closed()
	}
}

// await ждёт поток и запускает доставку; nil — ожидание снято (stop или закрытое соединение).
func (w *streamWait) await() jetstream.ConsumeContext {
	b, sc := w.b, w.sc
	ctx := context.Background()
	kv := []any{"stream", sc.stream, "subject", sc.filter, "durable", sc.durable}
	waiting := b.instr.attrs(attribute.String("stream", sc.stream))
	b.instr.waiting.Add(ctx, 1, waiting)
	defer b.instr.waiting.Add(ctx, -1, waiting)
	b.log.Warn(ctx, "natskit: stream not found, waiting for its author to create it", kv...)

	policy := b.streamWait
	begin := time.Now()
	remind := begin.Add(policy.remind)
	delay := policy.first
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return nil
		case <-b.c.closed:
			return nil
		case <-timer.C:
		}
		cc, err := w.attempt()
		switch {
		case cc != nil:
			b.log.Info(ctx, "natskit: awaited stream appeared, subscribed", append(kv, "waited", time.Since(begin).Round(time.Millisecond))...)
			return cc
		case w.ctx.Err() != nil:
			return nil
		case errors.Is(err, jetstream.ErrStreamNotFound):
			if now := time.Now(); !now.Before(remind) {
				b.log.Warn(ctx, "natskit: still waiting for stream", append(kv, "waited", now.Sub(begin).Round(time.Second))...)
				remind = now.Add(policy.remind)
			}
		default:
			b.log.Warn(ctx, "natskit: subscribe to the awaited stream failed, retrying", append(kv, "error", err)...)
		}
		delay = min(delay*2, policy.max)
		timer.Reset(delay)
	}
}

// attempt заводит консьюмер и запускает доставку под замком stop: снятое ожидание доставку не запускает.
func (w *streamWait) attempt() (jetstream.ConsumeContext, error) {
	ctx, cancel := context.WithTimeout(w.ctx, streamWaitAttempt)
	defer cancel()
	cons, err := w.b.js.CreateOrUpdateConsumer(ctx, w.sc.stream, w.b.consumerConfig(w.sc))
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return nil, context.Canceled
	}
	cc, err := w.b.consume(w.s, w.sc, cons, w.handler)
	if err != nil {
		return nil, err
	}
	w.cc = cc
	return cc, nil
}
