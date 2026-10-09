package sessiontest_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/testkit/sessiontest"
)

const brokenEnv = "SESSIONTEST_BROKEN"

// forgivingStore замечает повтор использованного токена, но сессию не отзывает: следующий токен
// цепочки остаётся жив — украденный токен продолжает работать.
type forgivingStore struct {
	session.Store
	mu   sync.Mutex
	used map[string]session.Token
}

func (s *forgivingStore) Rotate(ctx context.Context, refresh string) (session.Token, error) {
	s.mu.Lock()
	first, reused := s.used[refresh]
	s.mu.Unlock()
	if reused {
		return session.Token{}, &session.ReusedError{SessionID: first.SessionID, Subject: first.Subject}
	}
	next, err := s.Store.Rotate(ctx, refresh)
	if err == nil {
		s.mu.Lock()
		s.used[refresh] = next
		s.mu.Unlock()
	}
	return next, err
}

// Регрессия RTST-04: набор — мерка для боевых хранилищ, и заведомо сломанная реализация обязана
// его провалить — в том кейсе, который держит нарушенное правило. Кейс с ослабленным
// утверждением её пропустил бы; сам набор идёт в дочернем процессе этого же бинарника.
func TestRunContract_BrokenStoreFails(t *testing.T) {
	if os.Getenv(brokenEnv) != "" {
		sessiontest.RunContract(t, func(t *testing.T, cfg session.Config, clock clockkit.Clock) session.Store {
			s, err := sessiontest.New(cfg, clock)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return &forgivingStore{Store: s, used: map[string]session.Token{}}
		})
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunContract_BrokenStoreFails$", "-test.count=1")
	cmd.Env = append(os.Environ(), brokenEnv+"=reuse-keeps-the-session")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--- FAIL: TestRunContract_BrokenStoreFails/ReuseRevokesSession") {
		t.Fatalf("the contract must fail a store that keeps the session after a token reuse (err %v):\n%s", err, out)
	}
}
