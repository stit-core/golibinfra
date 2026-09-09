// Package bruteforcetest — фейки bruteforcekit.Guard для тестов сценариев
// логина без Postgres.
package bruteforcetest

import (
	"context"
	"sync"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// validateKey — контракт ключа pgstore без агрегации IPv6: пустой или слишком
// длинный subject, пустой либо неразбираемый ip — bruteforcekit.ErrInvalidKey.
// Без этой проверки юнит-тест сервиса не заметил бы проводку, которая отдаёт
// guard'у пустой ip и валится в проде.
func validateKey(subject, ip string) error {
	cfg := bruteforcekit.Config{IPv6Prefix: bruteforcekit.FullIPv6Prefix}
	_, _, err := cfg.NormalizeKey(subject, ip)
	return err
}

// Permissive — bruteforcekit.Guard, который никогда не блокирует. Ключ
// проверяется как в pgstore.
type Permissive struct{}

// Check разрешает попытку; невалидный ключ — bruteforcekit.ErrInvalidKey.
func (Permissive) Check(_ context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	return nil, validateKey(subject, ip)
}

// RecordFailure ничего не делает и блокировки не выставляет; невалидный ключ —
// bruteforcekit.ErrInvalidKey.
func (Permissive) RecordFailure(_ context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	return nil, validateKey(subject, ip)
}

// RecordSuccess ничего не делает; невалидный ключ — bruteforcekit.ErrInvalidKey.
func (Permissive) RecordSuccess(_ context.Context, subject, ip string) error {
	return validateKey(subject, ip)
}

// Locked — bruteforcekit.Guard, заблокированный до Until. Ключ проверяется как
// в pgstore.
type Locked struct {
	// Until — момент снятия блокировки, который отдают Check и RecordFailure.
	Until time.Time
}

// Check отдаёт блокировку до Until; невалидный ключ — bruteforcekit.ErrInvalidKey.
func (l Locked) Check(_ context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	if err := validateKey(subject, ip); err != nil {
		return nil, err
	}
	return &bruteforcekit.LockState{Until: l.Until}, nil
}

// RecordFailure отдаёт ту же блокировку, что и Check; невалидный ключ —
// bruteforcekit.ErrInvalidKey.
func (l Locked) RecordFailure(_ context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	if err := validateKey(subject, ip); err != nil {
		return nil, err
	}
	return &bruteforcekit.LockState{Until: l.Until}, nil
}

// RecordSuccess ничего не делает; невалидный ключ — bruteforcekit.ErrInvalidKey.
func (Locked) RecordSuccess(_ context.Context, subject, ip string) error {
	return validateKey(subject, ip)
}

// Attempt — одна зарегистрированная попытка: с каким ключом её позвал сервис.
type Attempt struct {
	// Subject — subject, переданный guard'у.
	Subject string
	// IP — ip, переданный guard'у.
	IP string
}

// Recorder — bruteforcekit.Guard, который пропускает всё и копит вызовы. Ключ
// проверяется как в pgstore, поэтому невалидная пара в историю не попадает.
type Recorder struct {
	mu        sync.Mutex
	failures  []Attempt
	successes []Attempt
}

// Check разрешает попытку; невалидный ключ — bruteforcekit.ErrInvalidKey.
func (r *Recorder) Check(_ context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	return nil, validateKey(subject, ip)
}

// RecordFailure запоминает пару (subject, ip); невалидный ключ —
// bruteforcekit.ErrInvalidKey.
func (r *Recorder) RecordFailure(_ context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	if err := validateKey(subject, ip); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, Attempt{Subject: subject, IP: ip})
	return nil, nil
}

// RecordSuccess запоминает пару (subject, ip); невалидный ключ —
// bruteforcekit.ErrInvalidKey.
func (r *Recorder) RecordSuccess(_ context.Context, subject, ip string) error {
	if err := validateKey(subject, ip); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.successes = append(r.successes, Attempt{Subject: subject, IP: ip})
	return nil
}

// Failures — копия subject'ов неудачных попыток в порядке записи.
func (r *Recorder) Failures() []string { return subjects(r.Attempts()) }

// Successes — копия subject'ов успешных попыток в порядке записи.
func (r *Recorder) Successes() []string { return subjects(r.SuccessAttempts()) }

// Attempts — копия неудачных попыток с ip: проверить, что сервис передал
// guard'у именно адрес клиента, по одному subject нельзя.
func (r *Recorder) Attempts() []Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Attempt(nil), r.failures...)
}

// SuccessAttempts — копия успешных попыток с ip.
func (r *Recorder) SuccessAttempts() []Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Attempt(nil), r.successes...)
}

func subjects(as []Attempt) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Subject
	}
	return out
}

// Counting — bruteforcekit.Guard в памяти с политикой pgstore: блокировка после
// MaxFailures неудач, истечение блокировки начинает новое окно, RecordSuccess
// стирает историю. Сценарий «N неудач → lock» проверяется без Postgres.
type Counting struct {
	cfg   bruteforcekit.Config
	clock clockkit.Clock

	mu   sync.Mutex
	rows map[Attempt]*countingRow
}

type countingRow struct {
	failedCount int
	lockedUntil time.Time
}

// NewCounting собирает Counting по конфигу кита; cfg валидируется, как в
// pgstore. clock == nil — clockkit.Real; clockkit.Tickable двигает время теста.
func NewCounting(cfg bruteforcekit.Config, clock clockkit.Clock) (*Counting, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = clockkit.Real{}
	}
	return &Counting{cfg: cfg, clock: clock, rows: map[Attempt]*countingRow{}}, nil
}

// Check отдаёт активную блокировку пары (subject, ip) либо nil.
func (c *Counting) Check(_ context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	key, err := c.key(subject, ip)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.rows[key]
	if !ok || !row.lockedUntil.After(c.clock.Now()) {
		return nil, nil
	}
	return &bruteforcekit.LockState{Until: row.lockedUntil, FailedCount: row.failedCount}, nil
}

// RecordFailure инкрементит счётчик окна и возвращает блокировку, если она
// активна после этой попытки.
func (c *Counting) RecordFailure(_ context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	key, err := c.key(subject, ip)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	row, ok := c.rows[key]
	switch {
	case !ok:
		row = &countingRow{}
		c.rows[key] = row
		row.failedCount = 1
	case !row.lockedUntil.IsZero() && !row.lockedUntil.After(now):
		row.failedCount = 1
		row.lockedUntil = time.Time{}
	default:
		row.failedCount++
	}
	if row.failedCount >= c.cfg.MaxFailures {
		row.lockedUntil = now.Add(c.cfg.LockDuration)
	}

	if !row.lockedUntil.After(now) {
		return nil, nil
	}
	return &bruteforcekit.LockState{Until: row.lockedUntil, FailedCount: row.failedCount}, nil
}

// RecordSuccess стирает историю попыток пары (subject, ip).
func (c *Counting) RecordSuccess(_ context.Context, subject, ip string) error {
	key, err := c.key(subject, ip)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rows, key)
	return nil
}

// Cleanup удаляет разблокированные строки — как Cleaner в pgstore, но без
// возраста: у фейка нет last_attempt.
func (c *Counting) Cleanup(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	for key, row := range c.rows {
		if !row.lockedUntil.After(now) {
			delete(c.rows, key)
		}
	}
	return nil
}

func (c *Counting) key(subject, ip string) (Attempt, error) {
	subject, ip, err := c.cfg.NormalizeKey(subject, ip)
	if err != nil {
		return Attempt{}, err
	}
	return Attempt{Subject: subject, IP: ip}, nil
}
