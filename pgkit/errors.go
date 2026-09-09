package pgkit

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrConfig — невалидный конфиг кита; возвращается Validate и конструкторами,
// которые его вызывают (NewPoolConfig, NewPool, NewReplicaConfig, …).
var ErrConfig = errors.New("pgkit: invalid config")

const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
	codeCheckViolation      = "23514"
	codeNotNullViolation    = "23502"
	codeSerializationFail   = "40001"
	codeDeadlockDetected    = "40P01"
	codeLockNotAvailable    = "55P03"
	codeQueryCanceled       = "57014"
)

// IsNoRows сообщает, что запрос не вернул строк (pgx.ErrNoRows).
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// IsUniqueViolation — 23505: нарушение unique-ограничения.
func IsUniqueViolation(err error) bool { return pgErrCode(err) == codeUniqueViolation }

// IsForeignKeyViolation — 23503: нарушение внешнего ключа.
func IsForeignKeyViolation(err error) bool { return pgErrCode(err) == codeForeignKeyViolation }

// IsCheckViolation — 23514: нарушение CHECK-ограничения.
func IsCheckViolation(err error) bool { return pgErrCode(err) == codeCheckViolation }

// IsNotNullViolation — 23502: NULL в NOT NULL-колонке.
func IsNotNullViolation(err error) bool { return pgErrCode(err) == codeNotNullViolation }

// IsLockNotAvailable — 55P03: NOWAIT или lock_timeout не дождались блокировки.
func IsLockNotAvailable(err error) bool { return pgErrCode(err) == codeLockNotAvailable }

// IsQueryCanceled — 57014: statement_timeout или pg_cancel_backend прервали запрос.
func IsQueryCanceled(err error) bool { return pgErrCode(err) == codeQueryCanceled }

// IsRetryable — 40001 (serialization failure) или 40P01 (deadlock detected):
// транзиентный конфликт, транзакцию имеет смысл повторить.
func IsRetryable(err error) bool {
	c := pgErrCode(err)
	return c == codeSerializationFail || c == codeDeadlockDetected
}

func pgErrCode(err error) string {
	if pg, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pg.Code
	}
	return ""
}
