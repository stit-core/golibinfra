package natskit

import (
	"fmt"

	"github.com/nats-io/nats.go"
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

// Close дренит подписки перед закрытием — сообщения in-flight дообрабатываются.
func (c *Conn) Close() {
	if c == nil || c.nc == nil {
		return
	}
	_ = c.nc.Drain()
	c.nc.Close()
}

func NewConn(cfg Config) (*Conn, error) {
	nc, err := nats.Connect(
		cfg.URL,
		nats.Timeout(cfg.ConnectTimeout),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.MaxReconnects(cfg.MaxReconnects),
	)
	if err != nil {
		return nil, fmt.Errorf("natskit: connect: %w", err)
	}
	return &Conn{nc: nc}, nil
}
