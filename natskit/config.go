package natskit

import "time"

type BackpressureConfig struct {
	MaxInFlight int `envconfig:"NATS_MAX_IN_FLIGHT" default:"100"`
}

type JetStreamConfig struct {
	Enabled     bool          `envconfig:"NATS_JETSTREAM_ENABLED" default:"false"`
	StreamNames []string      `envconfig:"NATS_JS_STREAM_NAMES" default:""`
	Retention   string        `envconfig:"NATS_JS_RETENTION" default:"limits"`
	MaxAge      time.Duration `envconfig:"NATS_JS_MAX_AGE" default:"168h"`
	MaxInFlight int           `envconfig:"NATS_JS_MAX_IN_FLIGHT" default:"100"`
	AckWait     time.Duration `envconfig:"NATS_JS_ACK_WAIT" default:"30s"`
}

type Config struct {
	URL            string        `envconfig:"NATS_URL" default:"nats://localhost:4222"`
	ConnectTimeout time.Duration `envconfig:"NATS_CONNECT_TIMEOUT" default:"5s"`
	ReconnectWait  time.Duration `envconfig:"NATS_RECONNECT_WAIT" default:"2s"`
	MaxReconnects  int           `envconfig:"NATS_MAX_RECONNECTS" default:"60"`
	Backpressure   BackpressureConfig
	JetStream      JetStreamConfig
}
