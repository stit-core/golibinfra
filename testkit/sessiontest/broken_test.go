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

// keepingStore на повторе в окне не гасит прежнего преемника: тот работает как выданный повтором — после повтора у
// сессии два живых токена, и второй держатель не обнаруживается.
type keepingStore struct {
	session.Store
	mu     sync.Mutex
	issued map[string]string // токен → последний выданный по нему преемник
	alias  map[string]string // преемник, которого повтор должен был погасить → выданный повтором
}

func (s *keepingStore) Rotate(ctx context.Context, refresh string) (session.Token, error) {
	s.mu.Lock()
	presented := refresh
	if live, ok := s.alias[refresh]; ok {
		refresh = live
	}
	s.mu.Unlock()
	next, err := s.Store.Rotate(ctx, refresh)
	if err == nil {
		s.mu.Lock()
		if previous, ok := s.issued[presented]; ok {
			s.alias[previous] = next.Value
		}
		s.issued[presented] = next.Value
		s.mu.Unlock()
	}
	return next, err
}

// Регрессия RTST-04: набор — мерка для боевых хранилищ, и заведомо сломанная реализация обязана
// его провалить — в том кейсе, который держит нарушенное правило. Кейс с ослабленным
// утверждением её пропустил бы; сам набор идёт в дочернем процессе этого же бинарника.
func TestRunContract_BrokenStoreFails(t *testing.T) {
	broken := map[string]struct {
		wrap    func(session.Store) session.Store
		failing string
	}{
		"reuse-keeps-the-session": {
			func(s session.Store) session.Store {
				return &forgivingStore{Store: s, used: map[string]session.Token{}}
			},
			"ReuseOutsideWindowRevokesSession",
		},
		"retry-keeps-the-successor": {
			func(s session.Store) session.Store {
				return &keepingStore{Store: s, issued: map[string]string{}, alias: map[string]string{}}
			},
			"RetryKillsPreviousSuccessor",
		},
		"retry-races-its-check": {
			func(s session.Store) session.Store { return sessiontest.RacyRetry(s.(*sessiontest.Store)) },
			"ConcurrentRetriesLeaveOneLiveToken",
		},
	}
	if variant := os.Getenv(brokenEnv); variant != "" {
		sessiontest.RunContract(t, func(t *testing.T, cfg session.Config, clock clockkit.Clock) session.Store {
			s, err := sessiontest.New(cfg, clock)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			return broken[variant].wrap(s)
		})
		return
	}
	for variant, b := range broken {
		t.Run(variant, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunContract_BrokenStoreFails$", "-test.count=1")
			cmd.Env = append(os.Environ(), brokenEnv+"="+variant)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "--- FAIL: TestRunContract_BrokenStoreFails/"+b.failing) {
				t.Fatalf("the contract must fail the %s store in %s (err %v):\n%s", variant, b.failing, err, out)
			}
		})
	}
}
