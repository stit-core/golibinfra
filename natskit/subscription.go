package natskit

import (
	"context"
	"sync"
	"sync/atomic"

	"git.stit.tech/stit-core/golibinfra/internal/stopbudget"
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
	// ctx — родитель ctx обработчиков подписки (ребёнок Conn.handlerCtx): cancel отменяет только
	// их — когда drain подписки исчерпал свой бюджет, а шина ещё открыта.
	ctx    context.Context
	cancel context.CancelFunc
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

func newSubscription(c *Conn, subject string) *subscription {
	ctx, cancel := context.WithCancel(c.handlerContext())
	return &subscription{conn: c, subject: subject, ctx: ctx, cancel: cancel}
}

// track учитывает запущенный обработчик; false — подписка снята Unsubscribe, и
// сообщение не обрабатывается.
func (s *subscription) track() (untrack func(), ok bool) {
	if s.unsubscribed.Load() {
		return nil, false
	}
	s.conn.inflight.add()
	s.inflight.add()
	untrack = func() {
		s.inflight.done()
		s.conn.inflight.done()
		s.release()
	}
	// Unsubscribe между проверкой и учётом уже освободил ctx подписки: обработчик не начинается.
	if s.unsubscribed.Load() {
		untrack()
		return nil, false
	}
	return untrack, true
}

// release освобождает ctx снятой подписки, когда её обработчики вернулись: ребёнок handlerCtx,
// не отменённый до закрытия шины, копился бы у сервиса, который подписывается и отписывается.
func (s *subscription) release() {
	if s.unsubscribed.Load() && s.inflight.count() == 0 {
		s.cancel()
	}
}

// abandon — drain не уложился в свой бюджет: работающим обработчикам подписки отменяется ctx.
func (s *subscription) abandon(ctx context.Context) {
	s.conn.log.Warn(ctx, "natskit: subscription drain timeout, handlers cancelled",
		"subject", s.subject, "inflight", s.inflight.count())
	s.cancel()
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
	s.release()
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
	s.cancel()
	return nil
}

// drainOnStop — Drain подписки на остановке fx за две трети остатка бюджета: треть остаётся
// хукам после (закрытие шины, пул, telemetry). Исчерпанная доля — не ошибка остановки:
// обработчикам подписки отменяется ctx, шину закрывает её хук.
func drainOnStop(ctx context.Context, sub Subscription) error {
	share, cancel := stopbudget.Share(ctx)
	defer cancel()
	err := sub.Drain(share)
	if s, ok := sub.(*subscription); ok && err != nil && share.Err() != nil {
		s.abandon(ctx)
		return nil
	}
	return err
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
