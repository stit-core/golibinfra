package authnkit

import (
	"testing"
)

func TestPermissions(t *testing.T) {
	rolesMu.Lock()
	oldRoles := roles
	roles = make(RolePermissions)
	rolesMu.Unlock()
	defer func() {
		rolesMu.Lock()
		roles = oldRoles
		rolesMu.Unlock()
	}()

	const (
		permRead  Permission = "articles:read"
		permWrite Permission = "articles:write"
	)

	RegisterRolePermissions("admin", permRead, permWrite)

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
		got := Has(tt.role, tt.perm)
		if got != tt.want {
			t.Errorf("Has(%q, %q) = %v, want %v", tt.role, tt.perm, got, tt.want)
		}
	}
}
