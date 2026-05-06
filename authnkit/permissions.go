package authnkit

import "sync"

type Permission string

type PermissionSet map[Permission]struct{}

type RolePermissions map[string]PermissionSet

var (
	rolesMu sync.RWMutex
	roles   = make(RolePermissions)
)

func RegisterRolePermissions(role string, perms ...Permission) {
	rolesMu.Lock()
	defer rolesMu.Unlock()
	ps := make(PermissionSet, len(perms))
	for _, p := range perms {
		ps[p] = struct{}{}
	}
	roles[role] = ps
}

func Has(role string, p Permission) bool {
	rolesMu.RLock()
	defer rolesMu.RUnlock()
	ps, ok := roles[role]
	if !ok {
		return false
	}
	_, has := ps[p]
	return has
}
