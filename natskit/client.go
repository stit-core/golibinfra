package natskit

import (
	"context"
	"fmt"

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

// New поднимает инстанс шины: валидирует конфиг, открывает соединение и
// декларирует стримы. Пустое имя — DefaultInstance.
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
	bus, err := NewBus(conn, log, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Client{name: name, conn: conn, bus: bus, stream: stream}, nil
}

// Name — имя инстанса (атрибут `instance` в сигналах).
func (c *Client) Name() string { return c.name }

// Bus — Core-шина инстанса.
func (c *Client) Bus() Bus { return c.bus }

// Stream — JetStream-шина инстанса (заглушка при выключенном JetStream).
func (c *Client) Stream() StreamBus { return c.stream }

// Conn — соединение инстанса.
func (c *Client) Conn() *Conn { return c.conn }

// Healthy — готовность соединения (см. Conn.Healthy).
func (c *Client) Healthy() error { return c.conn.Healthy() }

// Close — graceful: дренит подписки, ждёт обработчики (в пределах DrainTimeout)
// и закрывает соединение.
func (c *Client) Close() { c.conn.Close() }

// CloseContext — Close с бюджетом min(DrainTimeout, дедлайн ctx).
func (c *Client) CloseContext(ctx context.Context) { c.conn.CloseContext(ctx) }

// Probe — health-проба инстанса для fx-группы `healthkit.probes` (healthkit.Provide / HealthProbe).
func Probe(c *Client) healthkit.Probe {
	return healthkit.New("natskit/"+c.name, func(context.Context) error {
		if err := c.Healthy(); err != nil {
			return fmt.Errorf("natskit: healthy: %w", err)
		}
		return nil
	})
}
