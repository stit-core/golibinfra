package grpcclient

import "time"

// Config — публичный env-контракт клиента. Имена полей/тегов стабильны
// (README § Стабильность API). Грузится через config-композицию (ENV).
type Config struct {
	Target    string `envconfig:"GRPC_CLIENT_TARGET" required:"true"`
	Token     string `envconfig:"GRPC_CLIENT_TOKEN"`
	TLS       bool   `envconfig:"GRPC_CLIENT_TLS" default:"false"`
	Keepalive KeepaliveConfig
}

type KeepaliveConfig struct {
	Time    time.Duration `envconfig:"GRPC_CLIENT_KEEPALIVE_TIME" default:"30s"`
	Timeout time.Duration `envconfig:"GRPC_CLIENT_KEEPALIVE_TIMEOUT" default:"20s"`
}
