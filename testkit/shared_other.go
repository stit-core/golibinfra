//go:build !unix

package testkit

import "testing"

// lease — аренда сервера общего режима держится на flock: вне unix общего режима нет.
func lease(t testing.TB, _, kind string, _ int) int {
	t.Helper()
	t.Fatalf("testkit: shared %s: %s needs flock (unix)", kind, SharedEnv)
	return 0
}
