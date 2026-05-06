package natskit

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
	"go.uber.org/fx"
)

type Conn struct {
	nc *nats.Conn
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

func Probe(c *Conn) func(ctx context.Context) error {
	return func(_ context.Context) error {
		return c.Healthy()
	}
}

func newConn(lc fx.Lifecycle, cfg Config) (*Conn, error) {
	nc, err := nats.Connect(cfg.URL,
		nats.Timeout(cfg.ConnectTimeout),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.MaxReconnects(cfg.MaxReconnects),
	)
	if err != nil {
		return nil, fmt.Errorf("natskit: connect: %w", err)
	}

	conn := &Conn{nc: nc}

	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			_ = nc.Drain()
			nc.Close()
			return nil
		},
	})

	return conn, nil
}
