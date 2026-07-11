package oidckit

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

var ErrNoSessionCookie = errors.New("oidckit: no auth cookie")

// Extractor восстанавливает Identity защищённого запроса: cookie access_token →
// authnkit.JWTParser (тот же JWT_SECRET, что подписал BFF) → Identity в ctx.
// Сигнатура Extract совместима с httpkit.AuthExtractor.
type Extractor struct {
	parser authnkit.JWTParser
}

func NewExtractor(parser authnkit.JWTParser) *Extractor {
	return &Extractor{parser: parser}
}

func (e *Extractor) Extract(r *http.Request) (context.Context, error) {
	c, err := r.Cookie(CookieAccess)
	if err != nil || c.Value == "" {
		return r.Context(), ErrNoSessionCookie
	}

	claims, err := e.parser.Parse(c.Value)
	if err != nil {
		return r.Context(), fmt.Errorf("oidckit: parse session jwt: %w", err)
	}

	return WithIdentity(r.Context(), identityFromClaims(claims)), nil
}

func identityFromClaims(claims map[string]any) Identity {
	str := func(k string) string {
		s, _ := claims[k].(string)
		return s
	}
	return Identity{
		Subject:  str("sub"),
		Username: str("preferred_username"),
		Name:     str("name"),
		Email:    str("email"),
		Role:     str("role"),
	}
}
