package natskit

import natsgo "github.com/nats-io/nats.go"

func ExportConn(nc *natsgo.Conn) *Conn {
	return &Conn{nc: nc}
}
