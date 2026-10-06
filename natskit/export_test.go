package natskit

import (
	"time"

	natsgo "github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ExportConn оборачивает открытое тестом соединение в *Conn с DrainTimeout 10s (дефолт
// NATS_DRAIN_TIMEOUT); закрытие соединения тест контролирует сам.
func ExportConn(nc *natsgo.Conn) *Conn {
	return ExportConnWith(nc, logkit.Nop(), Config{DrainTimeout: 10 * time.Second})
}

func ExportConnWith(nc *natsgo.Conn, log logkit.Logger, cfg Config) *Conn {
	c := newConn(nc, log, cfg)
	nc.SetClosedHandler(c.onClosed)
	return c
}
