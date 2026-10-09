package idempotencytest_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/testkit/idempotencytest"
)

const brokenEnv = "IDEMPOTENCYTEST_BROKEN"

// ownerlessStore пишет запись, чей бы токен ни пришёл: запрос, переживший свой резерв, затирает
// резерв следующего.
type ownerlessStore struct {
	httpkit.IdempotencyStore
	mu     sync.Mutex
	tokens map[string]string
}

func (s *ownerlessStore) Reserve(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	token, ok, err := s.IdempotencyStore.Reserve(ctx, key, ttl)
	if ok {
		s.mu.Lock()
		s.tokens[key] = token
		s.mu.Unlock()
	}
	return token, ok, err
}

func (s *ownerlessStore) Set(ctx context.Context, key, _ string, record []byte, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	current := s.tokens[key]
	s.mu.Unlock()
	return s.IdempotencyStore.Set(ctx, key, current, record, ttl)
}

// Регрессия RTST-04: набор — мерка для своих store потребителя, и заведомо сломанная реализация
// обязана его провалить — в том кейсе, который держит нарушенное правило. Кейс с ослабленным
// утверждением её пропустил бы; сам набор идёт в дочернем процессе этого же бинарника.
func TestRunContract_BrokenStoreFails(t *testing.T) {
	if os.Getenv(brokenEnv) != "" {
		idempotencytest.RunContract(t, func(*testing.T) httpkit.IdempotencyStore {
			return &ownerlessStore{IdempotencyStore: httpkit.NewMemoryStore(), tokens: map[string]string{}}
		})
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunContract_BrokenStoreFails$", "-test.count=1")
	cmd.Env = append(os.Environ(), brokenEnv+"=set-ignores-the-token")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--- FAIL: TestRunContract_BrokenStoreFails/SetByForeignTokenIsRejected") {
		t.Fatalf("the contract must fail a store whose Set ignores the owner token (err %v):\n%s", err, out)
	}
}
