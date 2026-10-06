package grpcclient

import (
	"errors"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// ErrConfig — sentinel невалидной конфигурации клиента.
var ErrConfig = errors.New("grpcclient: invalid config")

// Config — публичный env-контракт клиента. Имена полей/тегов стабильны
// (README § Стабильность API). Нулевые Keepalive и лимиты сообщений означают
// дефолт; нулевой Timeout — «дедлайн только из ctx».
type Config struct {
	Target             string        `envconfig:"GRPC_CLIENT_TARGET" required:"true"`
	Token              string        `envconfig:"GRPC_CLIENT_TOKEN"`
	AllowInsecureToken bool          `envconfig:"GRPC_CLIENT_ALLOW_INSECURE_TOKEN" default:"false"`
	TLS                bool          `envconfig:"GRPC_CLIENT_TLS" default:"false"`
	TLSCAFile          string        `envconfig:"GRPC_CLIENT_TLS_CA_FILE"`
	TLSServerName      string        `envconfig:"GRPC_CLIENT_TLS_SERVER_NAME"`
	Timeout            time.Duration `envconfig:"GRPC_CLIENT_TIMEOUT" default:"10s"`
	MaxRecvMsgSize     int           `envconfig:"GRPC_CLIENT_MAX_RECV_MSG_SIZE" default:"4194304"`
	MaxSendMsgSize     int           `envconfig:"GRPC_CLIENT_MAX_SEND_MSG_SIZE" default:"4194304"`
	Keepalive          KeepaliveConfig
}

// KeepaliveConfig — keepalive-параметры HTTP/2-транспорта клиента.
type KeepaliveConfig struct {
	Time    time.Duration `envconfig:"GRPC_CLIENT_KEEPALIVE_TIME" default:"30s"`
	Timeout time.Duration `envconfig:"GRPC_CLIENT_KEEPALIVE_TIMEOUT" default:"20s"`
}

const (
	defaultMaxMsgSize     = 4 << 20
	defaultKeepaliveTime  = 30 * time.Second
	defaultKeepaliveTmout = 20 * time.Second
	// minKeepaliveTime — нижняя граница grpc-go (internal.KeepaliveMinPingTime):
	// меньшее значение молча клампится, поэтому отвергается на старте.
	minKeepaliveTime = 10 * time.Second
)

// NewConfig читает Config из ENV и валидирует его: ошибка конфигурации падает
// на старте приложения, а не на первом вызове.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// Validate отвергает конфигурации, при которых клиент заведомо неработоспособен
// (пустой target, отрицательные лимиты, keepalive ниже границы grpc-go) или
// небезопасен (bearer поверх plaintext без явного разрешения, TLS-параметры при
// выключенном TLS).
func (c Config) Validate() error {
	switch {
	case c.Target == "":
		return fmt.Errorf("%w: GRPC_CLIENT_TARGET is required", ErrConfig)
	case c.MaxRecvMsgSize < 0 || c.MaxSendMsgSize < 0:
		return fmt.Errorf("%w: GRPC_CLIENT_MAX_RECV_MSG_SIZE and GRPC_CLIENT_MAX_SEND_MSG_SIZE must be >= 0 (0 means default %d)", ErrConfig, defaultMaxMsgSize)
	case c.Timeout < 0:
		return fmt.Errorf("%w: GRPC_CLIENT_TIMEOUT must be >= 0 (0 means deadline from ctx only)", ErrConfig)
	case c.Keepalive.Time < 0 || c.Keepalive.Timeout < 0:
		return fmt.Errorf("%w: GRPC_CLIENT_KEEPALIVE_TIME and GRPC_CLIENT_KEEPALIVE_TIMEOUT must be >= 0", ErrConfig)
	case c.Keepalive.Time > 0 && c.Keepalive.Time < minKeepaliveTime:
		return fmt.Errorf("%w: GRPC_CLIENT_KEEPALIVE_TIME must be >= %s (grpc-go clamps lower values)", ErrConfig, minKeepaliveTime)
	case c.Token != "" && !c.TLS && !c.AllowInsecureToken:
		return fmt.Errorf("%w: GRPC_CLIENT_TOKEN over plaintext transport requires GRPC_CLIENT_TLS=true or GRPC_CLIENT_ALLOW_INSECURE_TOKEN=true", ErrConfig)
	case !c.TLS && (c.TLSCAFile != "" || c.TLSServerName != ""):
		return fmt.Errorf("%w: GRPC_CLIENT_TLS_CA_FILE and GRPC_CLIENT_TLS_SERVER_NAME require GRPC_CLIENT_TLS=true", ErrConfig)
	default:
		return nil
	}
}

// withDefaults подставляет дефолты нулевым полям, чтобы Config без ENV давал
// рабочий клиент, а не лимит сообщения в 0 байт.
func (c Config) withDefaults() Config {
	if c.MaxRecvMsgSize == 0 {
		c.MaxRecvMsgSize = defaultMaxMsgSize
	}
	if c.MaxSendMsgSize == 0 {
		c.MaxSendMsgSize = defaultMaxMsgSize
	}
	if c.Keepalive.Time == 0 {
		c.Keepalive.Time = defaultKeepaliveTime
	}
	if c.Keepalive.Timeout == 0 {
		c.Keepalive.Timeout = defaultKeepaliveTmout
	}
	return c
}
