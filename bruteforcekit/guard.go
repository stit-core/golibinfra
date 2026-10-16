// Package bruteforcekit — guard против brute-force на login-эндпойнты:
// учитывает попытки по паре (subject — логин/email, ip) и по subject на всех адресах до
// проверки секрета и блокирует пару после MaxFailures, а subject — после SubjectMaxFailures
// попыток без успеха; попытку, не дошедшую до проверки, возвращает Refund. Core-абстракции
// (Guard, Cleaner) отделены от pgstore-реализации.
package bruteforcekit

import (
	"context"
	"errors"
	"time"
)

// ErrInTx — Attempt вызван внутри транзакции вызывающего: её откат на неверном
// пароле снял бы учёт попытки, и перебор шёл бы без предела.
var ErrInTx = errors.New("bruteforcekit: Attempt must run outside a transaction")

// LockScope — что заблокировано.
type LockScope string

const (
	// ScopePair — пара (subject, ip): MaxFailures неудач с одного адреса.
	ScopePair LockScope = "pair"
	// ScopeSubject — subject на всех адресах: SubjectMaxFailures неудач с любых адресов.
	ScopeSubject LockScope = "subject"
)

// LockState — активная блокировка пары или subject'а; потребитель отвечает на обе одинаково.
type LockState struct {
	// Until — момент снятия блокировки по часам хранилища.
	Until time.Time
	// RetryAfter — остаток блокировки по часам хранилища: Retry-After ответа, не зависящий от
	// часов сервиса.
	RetryAfter time.Duration
	// FailedCount — попытки текущего окна без успешного входа на момент ответа.
	FailedCount int
	// Scope — пара или subject на всех адресах.
	Scope LockScope
}

// Guard — request-path guard'а: попытка учитывается до проверки секрета, успех
// снимает историю, попытку без проверки секрета возвращает Refund. Реализация в
// библиотеке — bruteforcekit/pgstore.
type Guard interface {
	// Attempt атомарно учитывает попытку пары (subject, ip) и subject'а до проверки секрета:
	// nil — попытка разрешена (одна из MaxFailures пары и SubjectMaxFailures субъекта в окне),
	// *LockState — пара или subject заблокированы, секрет не проверять. Попытка под блокировкой
	// счётчики не меняет. Вызывается вне транзакции (pgkit.InTx), иначе — ErrInTx.
	Attempt(ctx context.Context, subject, ip string) (*LockState, error)

	// RecordSuccess снимает историю попыток пары и окно субъекта: успех сбрасывает оба. В
	// транзакции допустим — её откат оставляет счётчики; строки он берёт в порядке Attempt.
	RecordSuccess(ctx context.Context, subject, ip string) error

	// Refund возвращает попытку, которую Attempt разрешил, а секрет так и не проверили (перегрузка проверки —
	// authnkit.ErrBusy): минус одна попытка окна пары и субъекта, и блокировку, которую эта попытка поставила на
	// пороге, снимает. Только после разрешённого Attempt и до проверки секрета: возврат проверенной попытки дал бы
	// перебору лишнюю. Истёкшую блокировку и историю, стёртую успехом, не трогает; в транзакции допустим — её
	// откат оставляет попытку учтённой; строки берёт в порядке Attempt.
	Refund(ctx context.Context, subject, ip string) error
}

// Cleaner — maintenance-путь guard'а, отдельный от request-path: его реализуют хранилища
// (pgstore.Store и фейк-хранилище bruteforcetest.Counting), а не адаптеры вокруг Guard.
type Cleaner interface {
	// Cleanup удаляет разблокированные строки старше Config.CleanupRetention, занятые попыткой
	// пропускает; идемпотентен, обычно вешается на периодический job.
	Cleanup(ctx context.Context) error
}
