package natskit

import (
	"strings"
	"time"
)

// DefaultInstance — имя дефолтного (безымянного) инстанса в метриках и спанах.
const DefaultInstance = "default"

type BackpressureConfig struct {
	MaxInFlight int `envconfig:"NATS_MAX_IN_FLIGHT" default:"100"`
}

type JetStreamConfig struct {
	Enabled     bool          `envconfig:"NATS_JETSTREAM_ENABLED" default:"false"`
	StreamNames []string      `envconfig:"NATS_JS_STREAM_NAMES" default:""`
	Retention   string        `envconfig:"NATS_JS_RETENTION" default:"limits"`
	MaxAge      time.Duration `envconfig:"NATS_JS_MAX_AGE" default:"168h"`
	Duplicates  time.Duration `envconfig:"NATS_JS_DUPLICATES" default:"2m"`
	MaxInFlight int           `envconfig:"NATS_JS_MAX_IN_FLIGHT" default:"100"`
	AckWait     time.Duration `envconfig:"NATS_JS_ACK_WAIT" default:"30s"`

	// Consumer redelivery / poison handling.
	//   MaxDeliver <= 0  — без ограничения (бесконечный ределивери, как раньше).
	//   NakBackoff  0    — мгновенный NAK; иначе NakWithDelay (backoff).
	//   DLQSubject  ""   — на исчерпании попыток только Term; иначе перед Term'ом
	//                      сообщение republish'ится в DLQSubject (best-effort).
	MaxDeliver int           `envconfig:"NATS_JS_MAX_DELIVER" default:"0"`
	NakBackoff time.Duration `envconfig:"NATS_JS_NAK_BACKOFF" default:"0"`
	DLQSubject string        `envconfig:"NATS_JS_DLQ_SUBJECT" default:""`
}

type Config struct {
	URL            string        `envconfig:"NATS_URL" default:"nats://localhost:4222"`
	ConnectTimeout time.Duration `envconfig:"NATS_CONNECT_TIMEOUT" default:"5s"`
	ReconnectWait  time.Duration `envconfig:"NATS_RECONNECT_WAIT" default:"2s"`
	MaxReconnects  int           `envconfig:"NATS_MAX_RECONNECTS" default:"60"`
	Backpressure   BackpressureConfig
	JetStream      JetStreamConfig

	// Instance — имя инстанса шины: едет атрибутом в метрики и спаны, чтобы
	// сигналы двух шин в одном процессе не сливались. Из ENV не читается,
	// проставляется New/Module.
	Instance string
}

// NewConfig читает конфиг дефолтного инстанса из `NATS_*`.
func NewConfig() (Config, error) {
	return NewConfigPrefixed("")
}

// NewConfigPrefixed читает конфиг инстанса из `<PREFIX>_NATS_*`. Голые `NATS_*`
// при этом игнорируются — набор переменных инстанса изолирован.
func NewConfigPrefixed(prefix string) (Config, error) {
	var c Config
	if err := loadEnv(prefix, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// envPrefix — префикс переменных окружения инстанса: `hub` → `HUB_NATS_URL`.
func envPrefix(instance string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(instance))
}
