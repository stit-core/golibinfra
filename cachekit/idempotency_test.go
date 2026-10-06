package cachekit_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/testkit/idempotencytest"
)

// Store обязан подходить httpkit.Idempotency без адаптера. Сам кит httpkit не
// импортирует (направление зависимостей), поэтому контракт сторожит тест.
var _ httpkit.IdempotencyStore = (*cachekit.IdempotencyStore)(nil)

// Один контракт с httpkit.MemoryStore и Redis-реализацией (KTST-10).
func TestIdempotencyStore_ContractOnMemory(t *testing.T) {
	idempotencytest.RunContract(t, func(*testing.T) httpkit.IdempotencyStore {
		return cachekit.NewIdempotencyStore(cachekit.NewMemoryCache())
	})
}
