package authnkit

import (
	"sync"
	"testing"
)

func TestRegistry(t *testing.T) {
	const (
		permRead  Permission = "articles:read"
		permWrite Permission = "articles:write"
	)

	r := NewRegistry()
	r.Register("admin", permRead, permWrite)

	tests := []struct {
		role string
		perm Permission
		want bool
	}{
		{"admin", permRead, true},
		{"admin", permWrite, true},
		{"admin", "unknown:perm", false},
		{"unknown-role", permRead, false},
		{"", permRead, false},
	}

	for _, tt := range tests {
		got := r.Has(tt.role, tt.perm)
		if got != tt.want {
			t.Errorf("Has(%q, %q) = %v, want %v", tt.role, tt.perm, got, tt.want)
		}
	}

	// Повторный Register полностью заменяет набор.
	r.Register("admin", permRead)
	if r.Has("admin", permWrite) {
		t.Fatal("Register must replace the role's permissions")
	}

	// Реестры независимы.
	if NewRegistry().Has("admin", permRead) {
		t.Fatal("registries must not share state")
	}
}

func TestRegistry_Concurrent(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			if i%2 == 0 {
				r.Register("viewer", "x:read")
			} else {
				_ = r.Has("viewer", "x:read")
			}
		})
	}
	wg.Wait()
}
