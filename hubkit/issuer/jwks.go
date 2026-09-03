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

// jwksCacheControl: набор меняется только при ротации, а next публикуется
// заранее — 5 минут кэша на прокси не задерживают ввод нового kid.
const jwksCacheControl = "public, max-age=300"

type jwksOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         jose.JSONWebKeySet
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
		return &jwksOutput{CacheControl: jwksCacheControl, Body: s.JWKS()}, nil
	})
}

// JWKSHandler — plain net/http вариант (для сервисов без huma на этом роуте).
func JWKSHandler(s *Signer) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", jwksCacheControl)
		_ = json.NewEncoder(w).Encode(s.JWKS())
	}
}
