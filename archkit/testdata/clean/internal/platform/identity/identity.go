// Package identity — личность вызывающего в ctx запроса.
package identity

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// Identity — кто вызывает usecase.
type Identity struct{ UserID string }

type ctxKey struct{}

// With кладёт личность в ctx.
func With(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// From достаёт личность из ctx.
func From(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// IPFrom — IP клиента из ctx запроса: домен читает его здесь, а не в транспорте.
func IPFrom(ctx context.Context) string { return httpkit.ClientIPFrom(ctx) }
