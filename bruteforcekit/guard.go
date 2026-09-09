// Package bruteforcekit — guard против brute-force на login-эндпойнты:
// считает неудачные попытки по паре (subject — логин/email, ip), при
// достижении порога блокирует пару на LockDuration. Core-абстракции
// (Guard, Cleaner) отделены от pgstore-реализации.
package bruteforcekit

import (
	"context"
	"time"
)

// LockState — активная блокировка пары (subject, ip).
type LockState struct {
	// Until — момент снятия блокировки по часам хранилища.
	Until time.Time
	// FailedCount — счётчик неудач текущего окна на момент ответа.
	FailedCount int
}

// Guard — request-path guard'а: проверка блокировки и учёт исхода попытки.
// Реализация в библиотеке — bruteforcekit/pgstore.
type Guard interface {
	// Check возвращает nil, если пара не заблокирована, и *LockState, пока
	// блокировка активна. Счётчик не меняет.
	Check(ctx context.Context, subject, ip string) (*LockState, error)

	// RecordFailure учитывает неудачную попытку и возвращает *LockState, если
	// после неё блокировка активна (nil — попытки ещё есть).
	RecordFailure(ctx context.Context, subject, ip string) (*LockState, error)

	// RecordSuccess снимает историю попыток пары.
	RecordSuccess(ctx context.Context, subject, ip string) error
}

// Cleaner — maintenance-путь guard'а, отдельный от request-path: его реализуют
// только хранилища, а не адаптеры и фейки вокруг Guard.
type Cleaner interface {
	// Cleanup удаляет разблокированные строки старше Config.CleanupRetention;
	// идемпотентен, обычно вешается на периодический job.
	Cleanup(ctx context.Context) error
}
