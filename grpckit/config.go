package grpckit

import (
	"errors"
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// ErrConfig — sentinel невалидной конфигурации сервера.
var ErrConfig = errors.New("grpckit: invalid config")

// Config — публичный env-контракт сервера. Имена полей/тегов стабильны
// (README § Стабильность API). Нулевое поле означает дефолт (см. docs/
// configuration.md), кроме HealthInterval и MaxConcurrentStreams, где 0 —
// «выключено» и «без лимита».
type Config struct {
	Addr                 string        `envconfig:"GRPC_ADDR" default:":9090"`
	MaxRecvMsgSize       int           `envconfig:"GRPC_MAX_RECV_MSG_SIZE" default:"4194304"`
	MaxSendMsgSize       int           `envconfig:"GRPC_MAX_SEND_MSG_SIZE" default:"4194304"`
	MaxConcurrentStreams uint32        `envconfig:"GRPC_MAX_CONCURRENT_STREAMS" default:"0"`
	Reflection           bool          `envconfig:"GRPC_REFLECTION" default:"false"`
	ShutdownGrace        time.Duration `envconfig:"GRPC_SHUTDOWN_GRACE" default:"10s"`
	HealthInterval       time.Duration `envconfig:"GRPC_HEALTH_INTERVAL" default:"10s"`
	HealthProbeTimeout   time.Duration `envconfig:"GRPC_HEALTH_PROBE_TIMEOUT" default:"5s"`
	TLSCertFile          string        `envconfig:"GRPC_TLS_CERT_FILE"`
	TLSKeyFile           string        `envconfig:"GRPC_TLS_KEY_FILE"`
	Keepalive            KeepaliveConfig
}

// KeepaliveConfig — keepalive-параметры HTTP/2-транспорта сервера.
type KeepaliveConfig struct {
	Time    time.Duration `envconfig:"GRPC_KEEPALIVE_TIME" default:"2h"`
	Timeout time.Duration `envconfig:"GRPC_KEEPALIVE_TIMEOUT" default:"20s"`
	MinTime time.Duration `envconfig:"GRPC_KEEPALIVE_MIN_TIME" default:"20s"`
}

const (
	defaultAddr       = ":9090"
	defaultMaxMsgSize = 4 << 20
	// defaultHealthProbeTimeout меньше дефолтного GRPC_HEALTH_INTERVAL (10s):
	// зависшая зависимость не должна растягивать цикл переоценки за его период.
	defaultHealthProbeTimeout = 5 * time.Second
	defaultShutdownGrace      = 10 * time.Second
	defaultKeepaliveTime      = 2 * time.Hour
	defaultKeepaliveTmout     = 20 * time.Second
	// defaultKeepaliveMin ≤ grpcclient.KeepaliveConfig.Time: клиент с дефолтами
	// не должен получать GOAWAY too_many_pings от сервера с дефолтами.
	defaultKeepaliveMin = 20 * time.Second
)

// NewConfig читает Config из ENV и валидирует его: ошибка конфигурации падает
// на старте приложения, а не на первом запросе.
func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, fmt.Errorf("grpckit: read config: %w", err)
	}
	return c, c.Validate()
}

// Validate отвергает значения, при которых сервер заведомо неработоспособен или
// небезопасен: отрицательные лимиты и интервалы, TLS с одной половиной пары
// cert/key (иначе сервер молча слушал бы plaintext). Нулевые поля валидны — их
// заменяет дефолт.
func (c Config) Validate() error {
	switch {
	case c.MaxRecvMsgSize < 0 || c.MaxSendMsgSize < 0:
		return fmt.Errorf("%w: GRPC_MAX_RECV_MSG_SIZE and GRPC_MAX_SEND_MSG_SIZE must be >= 0 (0 means default %d)", ErrConfig, defaultMaxMsgSize)
	case c.ShutdownGrace < 0:
		return fmt.Errorf("%w: GRPC_SHUTDOWN_GRACE must be >= 0 (0 means default %s)", ErrConfig, defaultShutdownGrace)
	case c.HealthInterval < 0:
		return fmt.Errorf("%w: GRPC_HEALTH_INTERVAL must be >= 0 (0 disables probe-driven health)", ErrConfig)
	case c.HealthProbeTimeout < 0:
		return fmt.Errorf("%w: GRPC_HEALTH_PROBE_TIMEOUT must be >= 0 (0 means default %s)", ErrConfig, defaultHealthProbeTimeout)
	case c.Keepalive.Time < 0 || c.Keepalive.Timeout < 0 || c.Keepalive.MinTime < 0:
		return fmt.Errorf("%w: GRPC_KEEPALIVE_TIME, GRPC_KEEPALIVE_TIMEOUT and GRPC_KEEPALIVE_MIN_TIME must be >= 0", ErrConfig)
	case (c.TLSCertFile == "") != (c.TLSKeyFile == ""):
		return fmt.Errorf("%w: GRPC_TLS_CERT_FILE and GRPC_TLS_KEY_FILE must be set both or neither", ErrConfig)
	default:
		return nil
	}
}

// withDefaults подставляет дефолты нулевым полям, чтобы zero-value Config давал
// рабочий сервер, а не лимит сообщения в 0 байт.
func (c Config) withDefaults() Config {
	if c.Addr == "" {
		c.Addr = defaultAddr
	}
	if c.MaxRecvMsgSize == 0 {
		c.MaxRecvMsgSize = defaultMaxMsgSize
	}
	if c.MaxSendMsgSize == 0 {
		c.MaxSendMsgSize = defaultMaxMsgSize
	}
	if c.ShutdownGrace == 0 {
		c.ShutdownGrace = defaultShutdownGrace
	}
	if c.HealthProbeTimeout == 0 {
		c.HealthProbeTimeout = defaultHealthProbeTimeout
	}
	if c.Keepalive.Time == 0 {
		c.Keepalive.Time = defaultKeepaliveTime
	}
	if c.Keepalive.Timeout == 0 {
		c.Keepalive.Timeout = defaultKeepaliveTmout
	}
	if c.Keepalive.MinTime == 0 {
		c.Keepalive.MinTime = defaultKeepaliveMin
	}
	return c
}
