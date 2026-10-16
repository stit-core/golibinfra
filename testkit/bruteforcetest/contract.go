package bruteforcetest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
)

// contractConfig — политика набора: порог пары 3, блокировки — час; subjectMax — порог субъекта
// (bruteforcekit.SubjectBudgetOff — без бюджета субъекта).
func contractConfig(subjectMax int) bruteforcekit.Config {
	return bruteforcekit.Config{
		MaxFailures:         3,
		LockDuration:        time.Hour,
		SubjectMaxFailures:  subjectMax,
		SubjectLockDuration: time.Hour,
		CleanupRetention:    24 * time.Hour,
		CleanupInterval:     5 * time.Minute,
		IPv6Prefix:          bruteforcekit.FullIPv6Prefix,
	}
}

// RunContract прогоняет контракт bruteforcekit.Guard — учёт попыток и их возврат: newGuard вызывается на
// каждый кейс и отдаёт guard с переданным Config; хранилище может быть общим для кейсов — subject у каждого
// кейса свой. Время кейсы не двигают: истечение блокировки проверяют тесты хранилища.
func RunContract(t *testing.T, newGuard func(t *testing.T, cfg bruteforcekit.Config) bruteforcekit.Guard) {
	t.Helper()
	cases := []struct {
		name       string
		subjectMax int
		run        func(t *testing.T, g bruteforcekit.Guard, subject string)
	}{
		{"LocksAfterMaxFailures", bruteforcekit.SubjectBudgetOff, locksAfterMaxFailures},
		{"RefundReturnsTheAttempt", bruteforcekit.SubjectBudgetOff, refundReturnsTheAttempt},
		{"RefundKeepsCheckedAttempts", bruteforcekit.SubjectBudgetOff, refundKeepsCheckedAttempts},
		{"RefundWithoutAttemptIsNoop", bruteforcekit.SubjectBudgetOff, refundWithoutAttempt},
		{"RefundAfterSuccessIsNoop", bruteforcekit.SubjectBudgetOff, refundAfterSuccess},
		{"RefundReturnsTheSubjectBudget", 3, refundReturnsTheSubjectBudget},
		{"RefundAfterParallelSuccessKeepsTheBudget", 3, refundAfterParallelSuccess},
		{"RefundRejectsInvalidKey", 3, refundRejectsInvalidKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			subject := "subject-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + "@example.com"
			c.run(t, newGuard(t, contractConfig(c.subjectMax)), subject)
		})
	}
}

const contractIP = "203.0.113.7"

func wantAllowed(t *testing.T, g bruteforcekit.Guard, subject, ip string, n int) {
	t.Helper()
	for i := range n {
		if st, err := g.Attempt(t.Context(), subject, ip); err != nil || st != nil {
			t.Fatalf("Attempt %d of %s from %s = %+v, %v; want allowed", i+1, subject, ip, st, err)
		}
	}
}

func wantLocked(t *testing.T, g bruteforcekit.Guard, subject, ip string, scope bruteforcekit.LockScope) {
	t.Helper()
	st, err := g.Attempt(t.Context(), subject, ip)
	if err != nil || st == nil || st.Scope != scope {
		t.Fatalf("Attempt of %s from %s = %+v, %v; want a %s lock", subject, ip, st, err, scope)
	}
}

func mustRefund(t *testing.T, g bruteforcekit.Guard, subject, ip string) {
	t.Helper()
	if err := g.Refund(t.Context(), subject, ip); err != nil {
		t.Fatalf("Refund(%s, %s): %v", subject, ip, err)
	}
}

func locksAfterMaxFailures(t *testing.T, g bruteforcekit.Guard, subject string) {
	wantAllowed(t, g, subject, contractIP, 3)
	wantLocked(t, g, subject, contractIP, bruteforcekit.ScopePair)
}

// Попытка, на которой счётчик достиг порога, поставила блокировку, а проверить секрет не вышло (перегрузка):
// возврат снимает её, окно снова пропускает одну попытку — и только одну.
func refundReturnsTheAttempt(t *testing.T, g bruteforcekit.Guard, subject string) {
	wantAllowed(t, g, subject, contractIP, 3)
	mustRefund(t, g, subject, contractIP)
	wantAllowed(t, g, subject, contractIP, 1)
	wantLocked(t, g, subject, contractIP, bruteforcekit.ScopePair)
}

// Возврат отдаёт только попытку без проверки секрета: сколько бы отказов перегрузки ни было между ними, к
// проверке успевают MaxFailures попыток, а следующая — под блокировкой.
func refundKeepsCheckedAttempts(t *testing.T, g bruteforcekit.Guard, subject string) {
	for range 3 {
		for range 5 {
			wantAllowed(t, g, subject, contractIP, 1)
			mustRefund(t, g, subject, contractIP)
		}
		wantAllowed(t, g, subject, contractIP, 1)
	}
	wantLocked(t, g, subject, contractIP, bruteforcekit.ScopePair)
}

// Возврат без учтённой попытки — у пустого окна и неизвестной пары — запаса не создаёт.
func refundWithoutAttempt(t *testing.T, g bruteforcekit.Guard, subject string) {
	mustRefund(t, g, subject, contractIP)
	mustRefund(t, g, subject, contractIP)
	locksAfterMaxFailures(t, g, subject)
}

// Успех стёр историю: возврат после него — пустой, окно не шире MaxFailures.
func refundAfterSuccess(t *testing.T, g bruteforcekit.Guard, subject string) {
	wantAllowed(t, g, subject, contractIP, 2)
	if err := g.RecordSuccess(t.Context(), subject, contractIP); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	mustRefund(t, g, subject, contractIP)
	locksAfterMaxFailures(t, g, subject)
}

// Бюджет субъекта возвращается так же: попытка с третьего адреса закрыла субъект, вернули её — субъект снова
// пропускает одну попытку с любого адреса.
func refundReturnsTheSubjectBudget(t *testing.T, g bruteforcekit.Guard, subject string) {
	for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
		wantAllowed(t, g, subject, ip, 1)
	}
	wantLocked(t, g, subject, "203.0.113.4", bruteforcekit.ScopeSubject)
	mustRefund(t, g, subject, "203.0.113.3")
	wantAllowed(t, g, subject, "203.0.113.4", 1)
	wantLocked(t, g, subject, "203.0.113.5", bruteforcekit.ScopeSubject)
}

// Возврат после параллельного успешного входа: попытку A учли, вход B того же субъекта с другого адреса успешен и
// обнулил окно субъекта, попытку C учли уже в новом окне, а A и C получили отказ перегрузки и вернулись. Возвратов в
// окне больше, чем учтённых в нём попыток, но окно не уходит ниже нуля: субъект снова пропускает SubjectMaxFailures
// попыток, не больше.
func refundAfterParallelSuccess(t *testing.T, g bruteforcekit.Guard, subject string) {
	wantAllowed(t, g, subject, "203.0.113.1", 1)
	wantAllowed(t, g, subject, "203.0.113.2", 1)
	if err := g.RecordSuccess(t.Context(), subject, "203.0.113.2"); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	wantAllowed(t, g, subject, "203.0.113.3", 1)
	mustRefund(t, g, subject, "203.0.113.1")
	mustRefund(t, g, subject, "203.0.113.3")
	for _, ip := range []string{"203.0.113.4", "203.0.113.5", "203.0.113.6"} {
		wantAllowed(t, g, subject, ip, 1)
	}
	wantLocked(t, g, subject, "203.0.113.7", bruteforcekit.ScopeSubject)
}

func refundRejectsInvalidKey(t *testing.T, g bruteforcekit.Guard, subject string) {
	for _, k := range [][2]string{{"", contractIP}, {subject, ""}, {subject, "not-an-ip"}, {subject + "\x00", contractIP}} {
		if err := g.Refund(t.Context(), k[0], k[1]); !errors.Is(err, bruteforcekit.ErrInvalidKey) {
			t.Errorf("Refund(%q, %q) = %v, want ErrInvalidKey", k[0], k[1], err)
		}
	}
}
