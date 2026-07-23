package sso

import (
	"context"
	"errors"
	"net/http"
)

var ErrNoSessionCookie = errors.New("hubkit/sso: no session cookie")

// Extractor восстанавливает Identity защищённого запроса: cookie stit_session →
// Verifier (ES256/JWKS) → Identity в ctx. Сигнатура Extract совместима с
// httpkit.AuthExtractor.
type Extractor struct {
	verifier *Verifier
	cookie   string
}

func NewExtractor(v *Verifier, cfg Config) *Extractor {
	cookie := cfg.Cookie
	if cookie == "" {
		cookie = "stit_session"
	}
	return &Extractor{verifier: v, cookie: cookie}
}

func (e *Extractor) Extract(r *http.Request) (context.Context, error) {
	c, err := r.Cookie(e.cookie)
	if err != nil || c.Value == "" {
		return r.Context(), ErrNoSessionCookie
	}
	id, err := e.verifier.Verify(r.Context(), c.Value)
	if err != nil {
		return r.Context(), err
	}
	return WithIdentity(r.Context(), id), nil
}
