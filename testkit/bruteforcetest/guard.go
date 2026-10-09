// Package bruteforcetest — фейки bruteforcekit.Guard для тестов сценариев
// логина без Postgres.
package bruteforcetest

import (
	"context"
	"maps"
	"sync"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
)

// validateKey — контракт ключа pgstore без агрегации IPv6: пустой subject, subject с NUL или не
// в UTF-8, пустой либо неразбираемый ip — bruteforcekit.ErrInvalidKey.
// Без этой проверки юнит-тест сервиса не заметил бы проводку, которая отдаёт
// guard'у пустой ip и валится в проде.
func validateKey(subject, ip string) error {
	cfg := bruteforcekit.Config{IPv6Prefix: bruteforcekit.FullIPv6Prefix}
	_, _, err := cfg.NormalizeKey(subject, ip)
	return err
}

// validateAttempt — контракт Attempt pgstore: вне транзакции (иначе
// bruteforcekit.ErrInTx) и с валидным ключом. Сервис, зовущий Attempt внутри
// Transactor.WithTx, краснеет в юнит-тесте с фейком pgtest, а не теряет учёт в проде.
func validateAttempt(ctx context.Context, subject, ip string) error {
	if pgkit.InTx(ctx) {
		return bruteforcekit.ErrInTx
	}
	return validateKey(subject, ip)
}

// Permissive — bruteforcekit.Guard, который никогда не блокирует. Ключ
// проверяется как в pgstore.
type Permissive struct{}

// Attempt разрешает попытку; в транзакции — bruteforcekit.ErrInTx, невалидный
// ключ — bruteforcekit.ErrInvalidKey.
func (Permissive) Attempt(ctx context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	return nil, validateAttempt(ctx, subject, ip)
}

// RecordSuccess ничего не делает; невалидный ключ — bruteforcekit.ErrInvalidKey.
func (Permissive) RecordSuccess(_ context.Context, subject, ip string) error {
	return validateKey(subject, ip)
}

// Locked — bruteforcekit.Guard, заблокированный до Until. Ключ проверяется как
// в pgstore.
type Locked struct {
	// Until — момент снятия блокировки, который отдаёт Attempt.
	Until time.Time
}

// Attempt отдаёт блокировку пары до Until (RetryAfter — остаток по часам процесса); в
// транзакции — bruteforcekit.ErrInTx, невалидный ключ — bruteforcekit.ErrInvalidKey.
func (l Locked) Attempt(ctx context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	if err := validateAttempt(ctx, subject, ip); err != nil {
		return nil, err
	}
	return &bruteforcekit.LockState{Until: l.Until, RetryAfter: max(time.Until(l.Until), 0), Scope: bruteforcekit.ScopePair}, nil
}

// RecordSuccess ничего не делает; невалидный ключ — bruteforcekit.ErrInvalidKey.
func (Locked) RecordSuccess(_ context.Context, subject, ip string) error {
	return validateKey(subject, ip)
}

// Attempt — один вызов guard'а: с каким ключом его позвал сервис.
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
	attempts  []Attempt
	successes []Attempt
}

// Attempt запоминает пару (subject, ip) и разрешает попытку; в транзакции —
// bruteforcekit.ErrInTx, невалидный ключ — bruteforcekit.ErrInvalidKey.
func (r *Recorder) Attempt(ctx context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	if err := validateAttempt(ctx, subject, ip); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, Attempt{Subject: subject, IP: ip})
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

// Attempts — копия вызовов Attempt в порядке записи: по ней тест сервиса
// видит, что попытка учтена и с каким ключом (адрес клиента).
func (r *Recorder) Attempts() []Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Attempt(nil), r.attempts...)
}

// Successes — копия вызовов RecordSuccess в порядке записи.
func (r *Recorder) Successes() []Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Attempt(nil), r.successes...)
}

// Counting — bruteforcekit.Guard в памяти с политикой pgstore: попытка
// учитывается до проверки секрета, пройти в окне успевают MaxFailures попыток пары и
// SubjectMaxFailures попыток subject'а со всех адресов, попытка под блокировкой счётчики не
// меняет, истечение блокировки начинает новое окно, RecordSuccess стирает историю пары и окно
// субъекта. Сценарий «N попыток → lock» проверяется без Postgres.
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

// Attempt учитывает попытку пары (subject, ip): nil — попытка разрешена,
// *LockState — пара заблокирована; в транзакции — bruteforcekit.ErrInTx.
func (c *Counting) Attempt(ctx context.Context, subject, ip string) (*bruteforcekit.LockState, error) {
	if pgkit.InTx(ctx) {
		return nil, bruteforcekit.ErrInTx
	}
	key, err := c.key(subject, ip)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	subjectKey := Attempt{Subject: key.Subject}
	budget := c.cfg.SubjectMaxFailures != bruteforcekit.SubjectBudgetOff
	if row := c.rows[subjectKey]; budget && row != nil && row.lockedUntil.After(now) {
		return row.lock(bruteforcekit.ScopeSubject, now), nil
	}
	if row := c.rows[key]; row != nil && row.lockedUntil.After(now) {
		return row.lock(bruteforcekit.ScopePair, now), nil
	}
	c.count(key, c.cfg.MaxFailures, c.cfg.LockDuration, now)
	if budget {
		c.count(subjectKey, c.cfg.SubjectMaxFailures, c.cfg.SubjectLockDuration, now)
	}
	return nil, nil
}

// count учитывает попытку в строке без активной блокировки: истёкшая начинает новое окно, порог
// ставит блокировку.
func (c *Counting) count(key Attempt, threshold int, lockFor time.Duration, now time.Time) {
	row, ok := c.rows[key]
	switch {
	case !ok:
		row = &countingRow{failedCount: 1}
		c.rows[key] = row
	case !row.lockedUntil.IsZero():
		row.failedCount = 1
		row.lockedUntil = time.Time{}
	default:
		row.failedCount++
	}
	if row.failedCount >= threshold {
		row.lockedUntil = now.Add(lockFor)
	}
}

func (r *countingRow) lock(scope bruteforcekit.LockScope, now time.Time) *bruteforcekit.LockState {
	return &bruteforcekit.LockState{Until: r.lockedUntil, RetryAfter: r.lockedUntil.Sub(now), FailedCount: r.failedCount, Scope: scope}
}

// RecordSuccess стирает историю попыток пары (subject, ip) и окно субъекта. В транзакции фейка
// pgtest её откат возвращает стёртое — как у pgstore: откат оставляет счётчики.
func (c *Counting) RecordSuccess(ctx context.Context, subject, ip string) error {
	key, err := c.key(subject, ip)
	if err != nil {
		return err
	}
	keys := []Attempt{key, {Subject: key.Subject}}

	c.mu.Lock()
	defer c.mu.Unlock()
	saved := map[Attempt]*countingRow{}
	for _, k := range keys {
		if row, ok := c.rows[k]; ok {
			saved[k] = row
		}
		delete(c.rows, k)
	}
	pgtest.OnRollback(ctx, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		maps.Copy(c.rows, saved)
	})
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
