package natskit_test

import (
	"context"
	"testing"

	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestSubjectFromContext_EmptyOutsideHandler(t *testing.T) {
	if s := natskit.SubjectFromContext(context.Background()); s != "" {
		t.Fatalf("SubjectFromContext on bare ctx = %q, want empty", s)
	}
}
