package bruteforcetest_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/testkit/bruteforcetest"
)

const brokenEnv = "BRUTEFORCETEST_BROKEN"

// forgetfulGuard возврат не делает: отказ перегрузки сжигает бюджет честного пользователя.
type forgetfulGuard struct{ bruteforcekit.Guard }

func (forgetfulGuard) Refund(context.Context, string, string) error { return nil }

// generousGuard возвращает две попытки за одну: к проверке секрета успевает больше MaxFailures.
type generousGuard struct{ bruteforcekit.Guard }

func (g generousGuard) Refund(ctx context.Context, subject, ip string) error {
	if err := g.Guard.Refund(ctx, subject, ip); err != nil {
		return err
	}
	return g.Guard.Refund(ctx, subject, ip)
}

// Набор — мерка для хранилищ: заведомо сломанный возврат обязан его провалить — в кейсе, который держит
// нарушенное правило. Сам набор идёт в дочернем процессе этого же бинарника.
func TestRunContract_BrokenGuardFails(t *testing.T) {
	broken := map[string]struct {
		wrap    func(bruteforcekit.Guard) bruteforcekit.Guard
		failing string
	}{
		"refund-does-nothing": {func(g bruteforcekit.Guard) bruteforcekit.Guard { return forgetfulGuard{g} }, "RefundReturnsTheAttempt"},
		"refund-returns-two":  {func(g bruteforcekit.Guard) bruteforcekit.Guard { return generousGuard{g} }, "RefundKeepsCheckedAttempts"},
	}
	if variant := os.Getenv(brokenEnv); variant != "" {
		bruteforcetest.RunContract(t, func(t *testing.T, cfg bruteforcekit.Config) bruteforcekit.Guard {
			g, err := bruteforcetest.NewCounting(cfg, nil)
			if err != nil {
				t.Fatalf("NewCounting: %v", err)
			}
			return broken[variant].wrap(g)
		})
		return
	}
	for variant, b := range broken {
		t.Run(variant, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunContract_BrokenGuardFails$", "-test.count=1")
			cmd.Env = append(os.Environ(), brokenEnv+"="+variant)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "--- FAIL: TestRunContract_BrokenGuardFails/"+b.failing) {
				t.Fatalf("the contract must fail the %s guard in %s (err %v):\n%s", variant, b.failing, err, out)
			}
		})
	}
}
