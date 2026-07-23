package grpckit

import "time"

// Config — публичный env-контракт сервера. Имена полей/тегов стабильны
// (README § Стабильность API). Грузится через config-композицию (ENV).
type Config struct {
	Addr          string        `envconfig:"GRPC_ADDR" default:":9090"`
	MaxRecvMiB    int           `envconfig:"GRPC_MAX_RECV_MIB" default:"4"`
	MaxSendMiB    int           `envconfig:"GRPC_MAX_SEND_MIB" default:"4"`
	Reflection    bool          `envconfig:"GRPC_REFLECTION" default:"false"`
	ShutdownGrace time.Duration `envconfig:"GRPC_SHUTDOWN_GRACE" default:"15s"`
	TLSCertFile   string        `envconfig:"GRPC_TLS_CERT_FILE"`
	TLSKeyFile    string        `envconfig:"GRPC_TLS_KEY_FILE"`
	Keepalive     KeepaliveConfig
}

type KeepaliveConfig struct {
	Time    time.Duration `envconfig:"GRPC_KEEPALIVE_TIME" default:"2h"`
	Timeout time.Duration `envconfig:"GRPC_KEEPALIVE_TIMEOUT" default:"20s"`
	MinTime time.Duration `envconfig:"GRPC_KEEPALIVE_MIN_TIME" default:"5m"`
}
