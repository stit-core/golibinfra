package idkit_test

import (
	"testing"

	"github.com/google/uuid"

	"git.stit.tech/stit-core/golibinfra/idkit"
)

func TestNewV7(t *testing.T) {
	id := idkit.NewV7()

	u, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("NewV7() = %q, not a valid uuid: %v", id, err)
	}
	if got := u.Version(); got != 7 {
		t.Fatalf("version = %d, want 7", got)
	}
}

func TestNewV7_Unique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := idkit.NewV7()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q at i=%d", id, i)
		}
		seen[id] = struct{}{}
	}
}
