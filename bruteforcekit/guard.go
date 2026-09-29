// Package bruteforcekit — guard против brute-force на login-эндпойнты:
// учитывает попытки по паре (subject — логин/email, ip) до проверки секрета и
// после MaxFailures попыток без успеха блокирует пару на LockDuration.
// Core-абстракции (Guard, Cleaner) отделены от pgstore-реализации.
package bruteforcekit

import (
	"context"
	"time"
)

// LockState — активная блокировка пары (subject, ip).
type LockState struct {
	// Until — момент снятия блокировки по часам хранилища.
	Until time.Time
	// FailedCount — попытки текущего окна без успешного входа на момент ответа.
	FailedCount int
}

// Guard — request-path guard'а: попытка учитывается до проверки секрета, успех
// снимает историю. Реализация в библиотеке — bruteforcekit/pgstore.
type Guard interface {
	// Attempt атомарно учитывает попытку пары (subject, ip) до проверки секрета:
	// nil — попытка разрешена (одна из MaxFailures в окне), *LockState — пара
	// заблокирована, секрет не проверять. Попытка под блокировкой счётчик не
	// меняет.
	Attempt(ctx context.Context, subject, ip string) (*LockState, error)

	// RecordSuccess снимает историю попыток пары: успех сбрасывает окно.
	RecordSuccess(ctx context.Context, subject, ip string) error
}

// Cleaner — maintenance-путь guard'а, отдельный от request-path: его реализуют
// только хранилища, а не адаптеры и фейки вокруг Guard.
type Cleaner interface {
	// Cleanup удаляет разблокированные строки старше Config.CleanupRetention;
	// идемпотентен, обычно вешается на периодический job.
	Cleanup(ctx context.Context) error
}
