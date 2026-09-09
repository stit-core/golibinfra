package natskit_test

import (
	"context"
	"testing"

	natsgo "github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestSubjectFromContext_EmptyOutsideHandler(t *testing.T) {
	if s := natskit.SubjectFromContext(context.Background()); s != "" {
		t.Fatalf("SubjectFromContext on bare ctx = %q, want empty", s)
	}
	if h := natskit.HeadersFromContext(context.Background()); h != nil {
		t.Fatalf("HeadersFromContext on bare ctx = %v, want nil", h)
	}
}

// ContextWithMessage — то же наполнение ctx, что кит даёт обработчику: на нём
// стоят фейки шины.
func TestContextWithMessage(t *testing.T) {
	ctx := natskit.ContextWithMessage(context.Background(), "orders.created",
		natsgo.Header{"Event-Type": []string{"order.created"}})

	if got := natskit.SubjectFromContext(ctx); got != "orders.created" {
		t.Fatalf("SubjectFromContext = %q", got)
	}
	if got := natskit.HeadersFromContext(ctx).Get("Event-Type"); got != "order.created" {
		t.Fatalf("Event-Type = %q", got)
	}

	bare := natskit.ContextWithMessage(context.Background(), "orders.created", nil)
	if h := natskit.HeadersFromContext(bare); h != nil {
		t.Fatalf("HeadersFromContext without headers = %v, want nil", h)
	}
}
