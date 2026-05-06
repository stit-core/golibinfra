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
)

func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func IsUniqueViolation(err error) bool     { return pgErrCode(err) == codeUniqueViolation }
func IsForeignKeyViolation(err error) bool { return pgErrCode(err) == codeForeignKeyViolation }
func IsCheckViolation(err error) bool      { return pgErrCode(err) == codeCheckViolation }
func IsNotNullViolation(err error) bool    { return pgErrCode(err) == codeNotNullViolation }

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
