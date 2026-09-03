// Package bruteforcekit — guard против brute-force на login-эндпойнты:
// считает неудачные попытки по паре (subject — логин/email, ip), при
// превышении порога возвращает LockState{Until}. Core-абстракция
// (Guard) отделена от pgstore-реализации.
package bruteforcekit

import (
	"context"
	"time"
)

type LockState struct {
	Until time.Time
}

type Guard interface {
	Check(ctx context.Context, subject, ip string) (*LockState, error)

	RecordFailure(ctx context.Context, subject, ip string) error

	RecordSuccess(ctx context.Context, subject, ip string) error

	// Cleanup удаляет разблокированные строки старше Config.CleanupRetention;
	// идемпотентен, обычно вешается на периодический job.
	Cleanup(ctx context.Context) error
}
