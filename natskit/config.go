package natskit

import (
	"strings"
	"time"
)

// DefaultInstance — имя дефолтного (безымянного) инстанса в метриках и спанах.
const DefaultInstance = "default"

const (
	defaultMaxInFlight         = 100
	defaultDrainTimeout        = 10 * time.Second
	defaultAckWait             = 30 * time.Second
	defaultMaxAge              = 7 * 24 * time.Hour
	defaultDuplicates          = 2 * time.Minute
	defaultPublishAsyncTimeout = 5 * time.Second
)

type BackpressureConfig struct {
	MaxInFlight int `envconfig:"NATS_MAX_IN_FLIGHT" default:"100"`
}

type JetStreamConfig struct {
	Enabled       bool          `envconfig:"NATS_JETSTREAM_ENABLED" default:"false"`
	StreamNames   []string      `envconfig:"NATS_JS_STREAM_NAMES" default:""`
	StreamManaged bool          `envconfig:"NATS_JS_STREAM_MANAGED" default:"true"`
	Retention     string        `envconfig:"NATS_JS_RETENTION" default:"limits"`
	MaxAge        time.Duration `envconfig:"NATS_JS_MAX_AGE" default:"168h"`
	Duplicates    time.Duration `envconfig:"NATS_JS_DUPLICATES" default:"2m"`
	MaxInFlight   int           `envconfig:"NATS_JS_MAX_IN_FLIGHT" default:"100"`
	AckWait       time.Duration `envconfig:"NATS_JS_ACK_WAIT" default:"30s"`
	// HandlerTimeout — deadline ctx одной попытки обработчика; 0 — равен AckWait.
	HandlerTimeout      time.Duration `envconfig:"NATS_JS_HANDLER_TIMEOUT" default:"0"`
	PublishAsyncTimeout time.Duration `envconfig:"NATS_JS_PUBLISH_ASYNC_TIMEOUT" default:"5s"`
	MaxDeliver          int           `envconfig:"NATS_JS_MAX_DELIVER" default:"0"`
	NakBackoff          time.Duration `envconfig:"NATS_JS_NAK_BACKOFF" default:"0"`
	DLQSubject          string        `envconfig:"NATS_JS_DLQ_SUBJECT" default:""`
}

type Config struct {
	URL            string        `envconfig:"NATS_URL" default:"nats://localhost:4222"`
	ConnectTimeout time.Duration `envconfig:"NATS_CONNECT_TIMEOUT" default:"5s"`
	ReconnectWait  time.Duration `envconfig:"NATS_RECONNECT_WAIT" default:"2s"`
	MaxReconnects  int           `envconfig:"NATS_MAX_RECONNECTS" default:"60"`
	DrainTimeout   time.Duration `envconfig:"NATS_DRAIN_TIMEOUT" default:"10s"`
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

func durationOr(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

func intOr(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}
