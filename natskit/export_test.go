package natskit

import (
	natsgo "github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ExportConn оборачивает открытое тестом соединение в *Conn с дефолтным
// DrainTimeout; закрытие соединения тест контролирует сам.
func ExportConn(nc *natsgo.Conn) *Conn {
	return ExportConnWith(nc, logkit.Nop(), Config{})
}

func ExportConnWith(nc *natsgo.Conn, log logkit.Logger, cfg Config) *Conn {
	c := newConn(nc, log, cfg)
	nc.SetClosedHandler(c.onClosed)
	return c
}
