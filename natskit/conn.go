package natskit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ErrClosed — подписка на инстанс, у которого уже начался Close.
var ErrClosed = errors.New("natskit: closed")

// Conn — соединение инстанса плюс реестр его подписок: Close дренит их и
// дожидается обработчиков, прежде чем закрыть сокет.
type Conn struct {
	nc           *nats.Conn
	log          logkit.Logger
	instance     string
	drainTimeout time.Duration
	closed       chan struct{}
	closedOnce   sync.Once

	mu       sync.Mutex
	closing  bool
	subs     map[*subscription]struct{}
	inflight inflightGroup
}

func NewConn(cfg Config, log logkit.Logger) (*Conn, error) {
	c := newConn(nil, log, cfg)
	nc, err := nats.Connect(
		cfg.URL,
		nats.Timeout(cfg.ConnectTimeout),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.DrainTimeout(c.drainTimeout),
		nats.ErrorHandler(c.onAsyncError),
		nats.DisconnectErrHandler(c.onDisconnect),
		nats.ReconnectHandler(c.onReconnect),
		nats.ClosedHandler(c.onClosed),
	)
	if err != nil {
		return nil, fmt.Errorf("natskit: connect: %w", err)
	}
	c.nc = nc
	return c, nil
}

func newConn(nc *nats.Conn, log logkit.Logger, cfg Config) *Conn {
	if log == nil {
		log = logkit.Nop()
	}
	instance := cfg.Instance
	if instance == "" {
		instance = DefaultInstance
	}
	return &Conn{
		nc:           nc,
		log:          log,
		instance:     instance,
		drainTimeout: durationOr(cfg.DrainTimeout, defaultDrainTimeout),
		closed:       make(chan struct{}),
		subs:         map[*subscription]struct{}{},
	}
}

func (c *Conn) raw() *nats.Conn { return c.nc }

func (c *Conn) Status() nats.Status { return c.nc.Status() }

func (c *Conn) Healthy() error {
	if c == nil || c.nc == nil {
		return fmt.Errorf("natskit: conn is nil")
	}
	if s := c.nc.Status(); s != nats.CONNECTED {
		return fmt.Errorf("natskit: not connected (status=%s)", s)
	}
	return nil
}

// Close дренит подписки, ждёт обработчики in-flight и закрывает соединение.
// Общий бюджет — DrainTimeout; по его истечении соединение закрывается
// принудительно, а незавершённые обработчики теряют возможность Ack.
func (c *Conn) Close() {
	if c == nil || c.nc == nil {
		return
	}
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return
	}
	c.closing = true
	subs := make([]*subscription, 0, len(c.subs))
	for s := range c.subs {
		subs = append(subs, s)
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), c.drainTimeout)
	defer cancel()

	for _, s := range subs {
		if err := s.request(true); err != nil {
			c.log.Warn(ctx, "natskit: subscription drain", "instance", c.instance, "subject", s.subject, "err", err)
		}
	}
	for _, s := range subs {
		if err := s.wait(ctx); err != nil {
			c.log.Warn(ctx, "natskit: subscription drain", "instance", c.instance, "subject", s.subject, "err", err)
		}
	}
	if err := c.inflight.wait(ctx); err != nil {
		c.log.Warn(ctx, "natskit: drain timeout, handlers still in flight",
			"instance", c.instance, "inflight", c.inflight.count(), "err", err)
	}

	if err := c.nc.Drain(); err != nil {
		c.nc.Close()
		return
	}
	select {
	case <-c.closed:
	case <-ctx.Done():
		c.log.Warn(ctx, "natskit: drain timeout, closing connection", "instance", c.instance)
		c.nc.Close()
	}
}

// subscribe регистрирует подписку атомарно относительно Close: после начала
// закрытия новых подписок нет, а начатые до него попадают в drain.
func (c *Conn) subscribe(build func() (*subscription, error)) (*subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil, ErrClosed
	}
	s, err := build()
	if err != nil {
		return nil, err
	}
	c.subs[s] = struct{}{}
	return s, nil
}

func (c *Conn) forget(s *subscription) {
	c.mu.Lock()
	delete(c.subs, s)
	c.mu.Unlock()
}

func (c *Conn) isClosing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closing
}

func (c *Conn) onAsyncError(_ *nats.Conn, sub *nats.Subscription, err error) {
	kv := []any{"instance", c.instance, "err", err}
	if sub != nil {
		kv = append(kv, "subject", sub.Subject)
		if dropped, derr := sub.Dropped(); derr == nil {
			kv = append(kv, "dropped", dropped)
		}
	}
	c.log.Error(context.Background(), "natskit: async error", kv...)
}

func (c *Conn) onDisconnect(_ *nats.Conn, err error) {
	if c.isClosing() {
		return
	}
	kv := []any{"instance", c.instance}
	if err != nil {
		kv = append(kv, "err", err)
	}
	c.log.Warn(context.Background(), "natskit: disconnected", kv...)
}

func (c *Conn) onReconnect(nc *nats.Conn) {
	c.log.Info(context.Background(), "natskit: reconnected", "instance", c.instance, "url", nc.ConnectedUrl())
}

func (c *Conn) onClosed(*nats.Conn) {
	c.closedOnce.Do(func() { close(c.closed) })
}
