package natskit

import (
	"context"
	"sync"
	"sync/atomic"
)

// Handler — обработчик входящего сообщения; subject и header'ы доступны через
// SubjectFromContext/HeadersFromContext.
type Handler func(ctx context.Context, payload []byte) error

// Subscription — handle одной подписки. Unsubscribe снимает интерес сразу,
// не дожидаясь обработчиков: доставка, которую транспорт отдаст после него, и
// полученные, но не начатые сообщения обработчик не получает (JetStream
// переотправит их после AckWait). Drain снимает интерес, даёт обработать уже
// полученное и ждёт завершения запущенных обработчиков в пределах ctx.
type Subscription interface {
	Unsubscribe() error
	Drain(ctx context.Context) error
}

type subscription struct {
	conn    *Conn
	subject string
	// stop останавливает транспортную подписку; drain=true — дообработать буфер.
	stop func(drain bool) error
	// done закрывает транспорт, когда подписка полностью закрыта: все callback'и
	// вернулись, новых add в inflight не будет.
	done     <-chan struct{}
	inflight inflightGroup
	// unsubscribed — Unsubscribe вызван: pull-подписку JetStream транспорт снимает
	// асинхронно, и доставки после возврата Unsubscribe ещё приходят в callback.
	unsubscribed atomic.Bool

	once    sync.Once
	stopErr error
}

// track учитывает запущенный обработчик; false — подписка снята Unsubscribe, и
// сообщение не обрабатывается.
func (s *subscription) track() (untrack func(), ok bool) {
	if s.unsubscribed.Load() {
		return nil, false
	}
	s.conn.inflight.add()
	s.inflight.add()
	return func() {
		s.inflight.done()
		s.conn.inflight.done()
	}, true
}

func (s *subscription) request(drain bool) error {
	s.once.Do(func() { s.stopErr = s.stop(drain) })
	return s.stopErr
}

func (s *subscription) wait(ctx context.Context) error {
	select {
	case <-s.done:
	case <-s.conn.closed:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.inflight.wait(ctx)
}

func (s *subscription) Unsubscribe() error {
	s.unsubscribed.Store(true)
	err := s.request(false)
	s.conn.forget(s)
	return err
}

func (s *subscription) Drain(ctx context.Context) error {
	if err := s.request(true); err != nil {
		return err
	}
	if err := s.wait(ctx); err != nil {
		return err
	}
	s.conn.forget(s)
	return nil
}

// inflightGroup — счётчик работающих обработчиков с ожиданием нуля. В отличие
// от sync.WaitGroup терпит add во время wait: подписка может доставить сообщение
// после истечения бюджета drain.
type inflightGroup struct {
	mu   sync.Mutex
	n    int
	idle chan struct{}
}

func (g *inflightGroup) add() {
	g.mu.Lock()
	g.n++
	g.mu.Unlock()
}

func (g *inflightGroup) done() {
	g.mu.Lock()
	g.n--
	if g.n == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
	g.mu.Unlock()
}

func (g *inflightGroup) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

func (g *inflightGroup) wait(ctx context.Context) error {
	g.mu.Lock()
	if g.n == 0 {
		g.mu.Unlock()
		return nil
	}
	if g.idle == nil {
		g.idle = make(chan struct{})
	}
	idle := g.idle
	g.mu.Unlock()

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
