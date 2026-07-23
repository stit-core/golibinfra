package sso

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Identity — principal hub-сессии, восстановленный Extractor'ом из session-JWT.
// EmployeeID — канонический hub-UUID сотрудника (claim employee_id), Subject —
// Authentik subject. Семантику Role kit не трактует — это делает сервис.
type Identity struct {
	EmployeeID string
	Subject    string
	Username   string // логин (напр. n.sycheva)
	Name       string // ФИО (напр. Наталья Сычева)
	Email      string
	Role       string
	SID        string // id сессии (для онлайн-отзыва критичных операций)
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
		panic("hubkit/sso: identity not in context")
	}
	return id
}

// RegisterIdentityLogAttr пробрасывает employee_id сессии в каждый log-record
// как user_id (fallback — subject, если employee_id пуст). Дёргается один раз на
// старте (Module делает это сам).
func RegisterIdentityLogAttr() {
	logkit.RegisterAttr(func(ctx context.Context) (string, any, bool) {
		id, ok := IdentityFrom(ctx)
		if !ok {
			return "", nil, false
		}
		uid := id.EmployeeID
		if uid == "" {
			uid = id.Subject
		}
		return "user_id", uid, true
	})
}
