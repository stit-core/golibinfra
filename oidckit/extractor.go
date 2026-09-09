package oidckit

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

// SessionTokenType — значение claim'а authnkit.ClaimTokenType у cookie-сессии
// BFF. Токен другой схемы (access/refresh парольного контура) под тем же
// JWT_SECRET сессией не является и в гейт не проходит.
const SessionTokenType = "oidc_session"

var (
	// ErrNoSessionCookie — в запросе нет session-cookie.
	ErrNoSessionCookie = errors.New("oidckit: no session cookie")
	// ErrInvalidSession — токен разобран, но это не session-JWT кита
	// (нет typ=oidc_session или пуст sub).
	ErrInvalidSession = errors.New("oidckit: invalid session token")
)

// Extractor восстанавливает Identity защищённого запроса: session-cookie →
// authnkit.JWTParser (тот же JWT_SECRET, что подписал BFF) → Identity в ctx.
// Сигнатура Extract совместима с httpkit.AuthExtractor.
type Extractor struct {
	parser authnkit.JWTParser
	cookie CookieConfig
}

// NewExtractor строит Extractor; cookie задаёт имя session-cookie (префикс
// `__Host-` при Secure).
func NewExtractor(parser authnkit.JWTParser, cookie CookieConfig) *Extractor {
	return &Extractor{parser: parser, cookie: cookie}
}

// Extract читает session-cookie и кладёт Identity в контекст запроса.
func (e *Extractor) Extract(r *http.Request) (context.Context, error) {
	c, err := r.Cookie(e.cookie.SessionCookieName())
	if err != nil || c.Value == "" {
		return r.Context(), ErrNoSessionCookie
	}

	claims, err := e.parser.Parse(r.Context(), c.Value)
	if err != nil {
		return r.Context(), fmt.Errorf("oidckit: parse session jwt: %w", err)
	}
	if typ, _ := claims[authnkit.ClaimTokenType].(string); typ != SessionTokenType {
		return r.Context(), fmt.Errorf("%w: unexpected %s", ErrInvalidSession, authnkit.ClaimTokenType)
	}
	id := identityFromClaims(claims)
	if id.Subject == "" {
		return r.Context(), fmt.Errorf("%w: no subject", ErrInvalidSession)
	}

	return WithIdentity(r.Context(), id), nil
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
