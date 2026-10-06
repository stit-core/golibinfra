package storagekit

import "slices"

// MemoryKeys — ключи объектов MemoryStorage по порядку: контрактный набор проверяет им, что
// отвергнутая загрузка ничего не оставила.
func MemoryKeys(m *MemoryStorage) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.objs))
	for k := range m.objs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// MemoryBytes — копия байтов объекта MemoryStorage по ключу.
func MemoryBytes(m *MemoryStorage, key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	obj, ok := m.objs[key]
	if !ok {
		return nil, false
	}
	return slices.Clone(obj.data), true
}
