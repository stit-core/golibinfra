package natskit

import "time"

type BackpressureConfig struct {
	MaxInFlight int `envconfig:"NATS_MAX_IN_FLIGHT" default:"100"`
}

type Config struct {
	URL            string        `envconfig:"NATS_URL" default:"nats://localhost:4222"`
	ConnectTimeout time.Duration `envconfig:"NATS_CONNECT_TIMEOUT" default:"5s"`
	ReconnectWait  time.Duration `envconfig:"NATS_RECONNECT_WAIT" default:"2s"`
	MaxReconnects  int           `envconfig:"NATS_MAX_RECONNECTS" default:"60"`
	Backpressure   BackpressureConfig
}
