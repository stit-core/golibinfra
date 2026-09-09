package storagetest_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/storagekit"
	"git.stit.tech/stit-core/golibinfra/testkit/storagetest"
)

// Контракт обязан проходить на in-memory реализации и без опционального Keys —
// иначе он не годится как эталон для боевых адаптеров.
func TestRunContract_MemoryStorageWithoutKeysHook(t *testing.T) {
	storagetest.RunContract(t, func(t *testing.T) storagetest.Subject {
		m := storagekit.NewMemoryStorage(storagekit.NewURLBuilder("https://cdn.example.com", "media", storagekit.URLStylePath))
		return storagetest.Subject{Storage: m, Presigner: m}
	})
}
