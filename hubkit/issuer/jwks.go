package issuer

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	jose "github.com/go-jose/go-jose/v4"
)

// JWKSPath — канонический путь публикации ключей (sso-verifier ходит именно сюда).
const JWKSPath = "/.well-known/jwks.json"

type jwksOutput struct {
	Body jose.JSONWebKeySet
}

// RegisterJWKS вешает GET /.well-known/jwks.json на huma-API: отдаёт публичные
// ключи (активный + next). Операция публичная (без Security).
func RegisterJWKS(api huma.API, s *Signer) {
	huma.Register(api, huma.Operation{
		OperationID: "hubJWKS",
		Method:      http.MethodGet,
		Path:        JWKSPath,
		Tags:        []string{"auth"},
		Summary:     "JWKS для верификации hub-session JWT",
	}, func(_ context.Context, _ *struct{}) (*jwksOutput, error) {
		return &jwksOutput{Body: s.JWKS()}, nil
	})
}

// JWKSHandler — plain net/http вариант (для сервисов без huma на этом роуте).
func JWKSHandler(s *Signer) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.JWKS())
	}
}
