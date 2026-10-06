package natskit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/internal/redact"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ErrClosed — подписка на инстанс, у которого уже начался Close.
var ErrClosed = errors.New("natskit: closed")

// ErrConnectionClosed — соединение закрыто окончательно (исчерпаны reconnect'ы
// или вызван Close); переподключения больше не будет. Отдаётся Healthy и Probe.
var ErrConnectionClosed = errors.New("natskit: connection closed")

// Conn — соединение инстанса плюс реестр его подписок: Close дренит их и
// дожидается обработчиков, прежде чем закрыть сокет.
type Conn struct {
	nc           *nats.Conn
	log          logkit.Logger
	instance     string
	url          string
	drainTimeout time.Duration
	closed       chan struct{}
	closedOnce   sync.Once

	// handlerCtx — общий родитель ctx всех обработчиков инстанса: Close отменяет
	// его, когда бюджет drain исчерпан, давая обработчикам шанс свернуться.
	handlerCtx     context.Context
	cancelHandlers context.CancelFunc

	mu       sync.Mutex
	closing  bool
	subs     map[*subscription]struct{}
	inflight inflightGroup
}

// NewConn открывает соединение инстанса и вешает на него логирование событий
// nats.go (disconnect/reconnect/async error/closed).
func NewConn(cfg Config, log logkit.Logger) (*Conn, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := newConn(nil, log, cfg)
	opts := []nats.Option{
		nats.Timeout(cfg.ConnectTimeout),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.DrainTimeout(c.drainTimeout),
		nats.ErrorHandler(c.onAsyncError),
		nats.DisconnectErrHandler(c.onDisconnect),
		nats.ReconnectHandler(c.onReconnect),
		nats.ClosedHandler(c.onClosed),
	}
	authOpts, err := authOptions(cfg)
	if err != nil {
		return nil, err
	}
	nc, err := nats.Connect(cfg.URL, append(opts, authOpts...)...)
	if err != nil {
		return nil, fmt.Errorf("natskit: connect %s: %w", c.url, err)
	}
	c.nc = nc
	return c, nil
}

func authOptions(cfg Config) ([]nats.Option, error) {
	var opts []nats.Option
	if cfg.CredsFile != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	}
	if cfg.NKeySeedFile != "" {
		opt, err := nats.NkeyOptionFromSeed(cfg.NKeySeedFile)
		if err != nil {
			return nil, fmt.Errorf("%w: NATS_NKEY_SEED_FILE: %w", ErrConfig, err)
		}
		opts = append(opts, opt)
	}
	if cfg.TLSCAFile != "" {
		opts = append(opts, nats.RootCAs(cfg.TLSCAFile))
	}
	return opts, nil
}

func newConn(nc *nats.Conn, log logkit.Logger, cfg Config) *Conn {
	if log == nil {
		log = logkit.Nop()
	}
	instance := cfg.Instance
	if instance == "" {
		instance = DefaultInstance
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Conn{
		nc:             nc,
		log:            log.With("instance", instance),
		instance:       instance,
		url:            redactURL(cfg.URL),
		drainTimeout:   cfg.DrainTimeout,
		closed:         make(chan struct{}),
		handlerCtx:     ctx,
		cancelHandlers: cancel,
		subs:           map[*subscription]struct{}{},
	}
}

func (c *Conn) raw() *nats.Conn { return c.nc }

// handlerContext — родитель ctx каждого обработчика: отменяется, когда Close
// исчерпал бюджет drain.
func (c *Conn) handlerContext() context.Context { return c.handlerCtx }

// Status — текущий статус соединения nats.go.
func (c *Conn) Status() nats.Status { return c.nc.Status() }

// Healthy сообщает, готово ли соединение принимать публикации: ErrConnectionClosed
// после окончательного закрытия (в т.ч. исчерпания reconnect'ов), иначе ошибка со
// статусом (RECONNECTING/DRAINING).
func (c *Conn) Healthy() error {
	if c == nil || c.nc == nil {
		return fmt.Errorf("%w: conn is nil", ErrConnectionClosed)
	}
	switch s := c.nc.Status(); s {
	case nats.CONNECTED:
		return nil
	case nats.CLOSED:
		if err := c.nc.LastError(); err != nil {
			return fmt.Errorf("%w: %w", ErrConnectionClosed, err)
		}
		return ErrConnectionClosed
	default:
		return fmt.Errorf("natskit: not connected (status=%s)", s)
	}
}

// Close дренит подписки, ждёт обработчики in-flight и закрывает соединение с
// бюджетом DrainTimeout.
func (c *Conn) Close() { c.CloseContext(context.Background()) }

// CloseContext — Close с бюджетом min(DrainTimeout, дедлайн ctx): OnStop-хук fx
// передаёт сюда свой ctx, чтобы shutdown укладывался в StopTimeout приложения.
// По истечении бюджета ctx обработчиков отменяется, а соединение закрывается
// принудительно — незавершённые обработчики теряют возможность Ack.
func (c *Conn) CloseContext(ctx context.Context) {
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
	defer c.cancelHandlers()

	ctx, cancel := context.WithTimeout(ctx, c.closeBudget(ctx))
	defer cancel()

	for _, s := range subs {
		if err := s.request(true); err != nil {
			c.log.Warn(ctx, "natskit: subscription drain", "subject", s.subject, "err", err)
		}
	}
	for _, s := range subs {
		if err := s.wait(ctx); err != nil {
			c.log.Warn(ctx, "natskit: subscription drain", "subject", s.subject, "err", err)
		}
	}
	if err := c.inflight.wait(ctx); err != nil {
		c.log.Warn(ctx, "natskit: drain timeout, handlers still in flight",
			"inflight", c.inflight.count(), "err", err)
		c.cancelHandlers()
	}

	if err := c.nc.Drain(); err != nil {
		c.nc.Close()
		return
	}
	select {
	case <-c.closed:
	case <-ctx.Done():
		c.log.Warn(ctx, "natskit: drain timeout, closing connection")
		c.nc.Close()
	}
}

func (c *Conn) closeBudget(ctx context.Context) time.Duration {
	budget := c.drainTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline); left < budget {
			budget = max(left, 0)
		}
	}
	return budget
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
	kv := []any{"err", err}
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
	kv := []any{"url", c.url}
	if err != nil {
		kv = append(kv, "err", err)
	}
	c.log.Warn(context.Background(), "natskit: disconnected", kv...)
}

func (c *Conn) onReconnect(nc *nats.Conn) {
	c.logReconnected(nc.ConnectedUrl())
}

func (c *Conn) logReconnected(url string) {
	c.log.Info(context.Background(), "natskit: reconnected", "url", redactURL(url))
}

// onClosed: закрытие без Close — это исчерпанные reconnect'ы; подписки мертвы
// навсегда, поэтому событие уровня error, а не тишина.
func (c *Conn) onClosed(nc *nats.Conn) {
	c.closedOnce.Do(func() { close(c.closed) })
	if c.isClosing() {
		return
	}
	kv := []any{"url", c.url}
	if nc != nil {
		if err := nc.LastError(); err != nil {
			kv = append(kv, "err", err)
		}
	}
	c.log.Error(context.Background(), "natskit: connection closed, reconnects exhausted", kv...)
}

// redactURL — NATS_URL (список через запятую) без секретов: пароль и токен не попадают ни в логи,
// ни в текст ошибок — и у адреса без схемы (`user:pass@host:4222`).
func redactURL(raw string) string {
	parts := strings.Split(raw, ",")
	for i, part := range parts {
		parts[i] = redact.URL(strings.TrimSpace(part))
	}
	return strings.Join(parts, ",")
}
