package oidckit

import (
	"context"
	"log/slog"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Identity — principal cookie-сессии, восстановленный Extractor'ом из session-JWT.
// Role приезжает из claim'а "role" (его кладёт Hooks.SessionClaims консьюмера);
// семантика значений — на стороне сервиса, kit ролей не трактует.
type Identity struct {
	Subject  string
	Username string // preferred_username — логин (напр. j.doe)
	Name     string // name — отображаемое имя (напр. Jane Doe)
	Email    string
	Role     string
}

type identityCtxKey struct{}

// WithIdentity кладёт Identity в контекст запроса.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// IdentityFrom достаёт Identity из контекста; ok=false — запрос не прошёл Extractor.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(Identity)
	return id, ok
}

// MustIdentityFrom — Identity защищённой операции; паника, если её нет
// (операция без Security или без AuthExtractor — ошибка проводки).
func MustIdentityFrom(ctx context.Context) Identity {
	id, ok := IdentityFrom(ctx)
	if !ok {
		panic("oidckit: identity not in context")
	}
	return id
}

// RegisterIdentityLogAttr пробрасывает subject сессии в каждый log-record как
// user_id; повторный вызов заменяет регистрацию, а не дублирует поле.
func RegisterIdentityLogAttr() {
	logkit.RegisterAttr("user_id", func(ctx context.Context) (slog.Value, bool) {
		id, ok := IdentityFrom(ctx)
		if !ok {
			return slog.Value{}, false
		}
		return slog.StringValue(id.Subject), true
	})
}
