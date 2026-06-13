package grpcclient

import "context"

// bearerPerRPC инъектит access-токен (из env) в authorization-metadata на каждый
// вызов — это «токен через env» у сервиса-потребителя гейтвея. secure=false
// разрешает per-RPC creds поверх plaintext (внутренний mesh; mTLS — на sidecar).
type bearerPerRPC struct {
	token  string
	secure bool
}

func (b bearerPerRPC) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}

func (b bearerPerRPC) RequireTransportSecurity() bool { return b.secure }
