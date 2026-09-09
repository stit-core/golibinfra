package issuer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	jose "github.com/go-jose/go-jose/v4"
)

// JWKSPath — канонический путь публикации ключей (sso-verifier ходит именно сюда).
const JWKSPath = "/.well-known/jwks.json"

type jwksOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         jose.JSONWebKeySet
}

// RegisterJWKS вешает GET /.well-known/jwks.json на huma-API: отдаёт публичные
// ключи (активный + next + previous) с Cache-Control: max-age из
// HUB_SESSION_JWKS_MAX_AGE. Операция публичная (без Security).
func RegisterJWKS(api huma.API, s *Signer) {
	cacheControl := cacheControlFor(s)
	huma.Register(api, huma.Operation{
		OperationID: "hubJWKS",
		Method:      http.MethodGet,
		Path:        JWKSPath,
		Tags:        []string{"auth"},
		Summary:     "JWKS for hub-session JWT verification",
	}, func(_ context.Context, _ *struct{}) (*jwksOutput, error) {
		return &jwksOutput{CacheControl: cacheControl, Body: s.JWKS()}, nil
	})
}

// JWKSHandler — plain net/http вариант (для сервисов без huma на этом роуте).
func JWKSHandler(s *Signer) http.HandlerFunc {
	cacheControl := cacheControlFor(s)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", cacheControl)
		_ = json.NewEncoder(w).Encode(s.JWKS())
	}
}

func cacheControlFor(s *Signer) string {
	return fmt.Sprintf("public, max-age=%d", int(s.JWKSMaxAge().Seconds()))
}
