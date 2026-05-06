package pgkit

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func FuzzPgErrCode(f *testing.F) {
	f.Add("23505")
	f.Add("")
	f.Add("XXYYZ")
	f.Add("40001")

	f.Fuzz(func(t *testing.T, code string) {
		base := &pgconn.PgError{Code: code}
		wrapped := fmt.Errorf("layer: %w", base)

		_ = IsUniqueViolation(wrapped)
		_ = IsForeignKeyViolation(wrapped)
		_ = IsCheckViolation(wrapped)
		_ = IsNotNullViolation(wrapped)
		_ = IsRetryable(wrapped)
		_ = IsNoRows(wrapped)
	})
}
