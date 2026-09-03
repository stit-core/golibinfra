package pgkit

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

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

func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func IsUniqueViolation(err error) bool     { return pgErrCode(err) == codeUniqueViolation }
func IsForeignKeyViolation(err error) bool { return pgErrCode(err) == codeForeignKeyViolation }
func IsCheckViolation(err error) bool      { return pgErrCode(err) == codeCheckViolation }
func IsNotNullViolation(err error) bool    { return pgErrCode(err) == codeNotNullViolation }

// IsLockNotAvailable — 55P03: NOWAIT или lock_timeout не дождались блокировки.
func IsLockNotAvailable(err error) bool { return pgErrCode(err) == codeLockNotAvailable }

// IsQueryCanceled — 57014: statement_timeout или pg_cancel_backend прервали запрос.
func IsQueryCanceled(err error) bool { return pgErrCode(err) == codeQueryCanceled }

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
