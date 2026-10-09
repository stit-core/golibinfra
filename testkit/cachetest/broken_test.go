package cachetest_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/testkit/cachetest"
)

const brokenEnv = "CACHETEST_BROKEN"

// blindCache пишет в CompareAndSet поверх любого значения ключа: чужая запись затирается.
type blindCache struct{ cachekit.Cache }

func (c blindCache) CompareAndSet(ctx context.Context, key string, _, value []byte, ttl time.Duration) (bool, error) {
	// Ключа нет — писать не поверх чего, как у исправного кэша.
	if _, found, err := c.Get(ctx, key); err != nil || !found {
		return false, err
	}
	return true, c.Set(ctx, key, value, ttl)
}

// Регрессия RTST-04: набор — мерка для своих кэшей потребителя, и заведомо сломанная реализация
// обязана его провалить — в том кейсе, который держит нарушенное правило. Кейс с ослабленным
// утверждением её пропустил бы; сам набор идёт в дочернем процессе этого же бинарника.
func TestRunContract_BrokenCacheFails(t *testing.T) {
	if os.Getenv(brokenEnv) != "" {
		cachetest.RunContract(t, func(*testing.T) cachekit.Cache { return blindCache{cachekit.NewMemoryCache()} })
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunContract_BrokenCacheFails$", "-test.count=1")
	cmd.Env = append(os.Environ(), brokenEnv+"=compare-and-set-is-blind")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--- FAIL: TestRunContract_BrokenCacheFails/CompareAndSet") {
		t.Fatalf("the contract must fail a cache whose CompareAndSet ignores the expected value (err %v):\n%s", err, out)
	}
}
