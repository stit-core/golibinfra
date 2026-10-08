package cachetest_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/testkit/cachetest"
)

// Контракт обязан проходить на in-memory кэше кита — иначе он не годится как мерка для своих
// реализаций потребителя; на Redis его гоняет cachekit.
func TestRunContract_MemoryCache(t *testing.T) {
	cachetest.RunContract(t, func(*testing.T) cachekit.Cache { return cachekit.NewMemoryCache() })
}
