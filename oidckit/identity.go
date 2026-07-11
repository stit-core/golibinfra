package oidckit

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Identity — principal cookie-сессии, восстановленный Extractor'ом из session-JWT.
// Role приезжает из claim'а "role" (его кладёт Hooks.SessionClaims консьюмера);
// семантика значений — на стороне сервиса, kit ролей не трактует.
type Identity struct {
	Subject  string
	Username string // preferred_username — логин (напр. n.sycheva)
	Name     string // name — ФИО (напр. Наталья Сычева)
	Email    string
	Role     string
}

type identityCtxKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(Identity)
	return id, ok
}

func MustIdentityFrom(ctx context.Context) Identity {
	id, ok := IdentityFrom(ctx)
	if !ok {
		panic("oidckit: identity not in context")
	}
	return id
}

// RegisterIdentityLogAttr пробрасывает subject сессии в каждый log-record
// как user_id (logkit user-registered attr). Дергается один раз на старте
// (Module делает это сам).
func RegisterIdentityLogAttr() {
	logkit.RegisterAttr(func(ctx context.Context) (string, any, bool) {
		id, ok := IdentityFrom(ctx)
		if !ok {
			return "", nil, false
		}
		return "user_id", id.Subject, true
	})
}
