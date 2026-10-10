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

// ownerlessStore подменяет токен вызова токеном текущего резерва — у Set или у Release: чей бы
// токен ни пришёл, запрос, переживший свой резерв, затирает или снимает резерв следующего.
type ownerlessStore struct {
	httpkit.IdempotencyStore
	// set и release — у какой операции токен вызова ничего не решает.
	set, release bool
	mu           sync.Mutex
	tokens       map[string]string
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

func (s *ownerlessStore) owner(key, token string, ignored bool) string {
	if !ignored {
		return token
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[key]
}

func (s *ownerlessStore) Set(ctx context.Context, key, token string, record []byte, ttl time.Duration) (bool, error) {
	return s.IdempotencyStore.Set(ctx, key, s.owner(key, token, s.set), record, ttl)
}

func (s *ownerlessStore) Release(ctx context.Context, key, token string) error {
	return s.IdempotencyStore.Release(ctx, key, s.owner(key, token, s.release))
}

// Регрессия RTST-04: набор — мерка для своих store потребителя, и заведомо сломанная реализация
// обязана его провалить — в том кейсе, который держит нарушенное правило: и Set, и Release чужим
// токеном. Кейс с ослабленным утверждением её пропустил бы; сам набор идёт в дочернем процессе
// этого же бинарника.
func TestRunContract_BrokenStoreFails(t *testing.T) {
	if rule := os.Getenv(brokenEnv); rule != "" {
		idempotencytest.RunContract(t, func(*testing.T) httpkit.IdempotencyStore {
			return &ownerlessStore{IdempotencyStore: httpkit.NewMemoryStore(), tokens: map[string]string{}, set: rule == "Set", release: rule == "Release"}
		})
		return
	}
	for rule, failed := range map[string]string{"Set": "SetByForeignTokenIsRejected", "Release": "ReleaseOnlyByOwner"} {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunContract_BrokenStoreFails$", "-test.count=1")
		cmd.Env = append(os.Environ(), brokenEnv+"="+rule)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "--- FAIL: TestRunContract_BrokenStoreFails/"+failed) {
			t.Errorf("the contract must fail a store whose %s ignores the owner token, in %s (err %v):\n%s", rule, failed, err, out)
		}
	}
}
