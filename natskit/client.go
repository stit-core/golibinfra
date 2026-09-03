package natskit

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Client — инстанс шины: соединение плюс Bus и StreamBus поверх него. Инстансов
// в процессе может быть несколько (своя шина проекта и внешняя шина-интегратор),
// каждый со своим Config и именем.
type Client struct {
	name   string
	conn   *Conn
	bus    Bus
	stream StreamBus
}

func New(name string, cfg Config, log logkit.Logger) (*Client, error) {
	if name == "" {
		name = DefaultInstance
	}
	cfg.Instance = name

	conn, err := NewConn(cfg, log)
	if err != nil {
		return nil, err
	}
	stream, err := NewStreamBus(conn, log, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Client{name: name, conn: conn, bus: NewBus(conn, log, cfg), stream: stream}, nil
}

func (c *Client) Name() string { return c.name }

func (c *Client) Bus() Bus { return c.bus }

func (c *Client) Stream() StreamBus { return c.stream }

func (c *Client) Conn() *Conn { return c.conn }

func (c *Client) Healthy() error { return c.conn.Healthy() }

// Close — graceful: дренит подписки, ждёт обработчики (в пределах DrainTimeout)
// и закрывает соединение.
func (c *Client) Close() { c.conn.Close() }

// Probe — health-проба инстанса для fx-группы `health` (healthkit.Provide / HealthProbe).
func Probe(c *Client) healthkit.Probe {
	return func(context.Context) error {
		return c.Healthy()
	}
}
