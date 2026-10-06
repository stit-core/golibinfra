package idempotencytest_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/testkit/idempotencytest"
)

// Контракт обязан проходить на эталонном in-memory store httpkit — иначе он не годится как мерка
// для боевых.
func TestRunContract_MemoryStore(t *testing.T) {
	idempotencytest.RunContract(t, func(*testing.T) httpkit.IdempotencyStore { return httpkit.NewMemoryStore() })
}
