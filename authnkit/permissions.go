package authnkit

import "sync"

type Permission string

// Registry — реестр «роль → права»: наполняется на старте приложения,
// проверки — под RLock. Неизвестная роль или право → false (fail-closed).
type Registry struct {
	mu    sync.RWMutex
	roles map[string]map[Permission]struct{}
}

func NewRegistry() *Registry {
	return &Registry{roles: map[string]map[Permission]struct{}{}}
}

// Register полностью заменяет набор прав роли.
func (r *Registry) Register(role string, perms ...Permission) {
	set := make(map[Permission]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roles[role] = set
}

func (r *Registry) Has(role string, p Permission) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.roles[role][p]
	return ok
}
