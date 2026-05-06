package pgkit

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func pgErr(code string) *pgconn.PgError {
	return &pgconn.PgError{Code: code}
}

func TestIsUniqueViolation(t *testing.T) {
	err := pgErr("23505")
	if !IsUniqueViolation(err) {
		t.Fatal("expected IsUniqueViolation true")
	}
	if IsForeignKeyViolation(err) || IsCheckViolation(err) || IsNotNullViolation(err) || IsRetryable(err) {
		t.Fatal("expected all others false for 23505")
	}
}

func TestIsForeignKeyViolation(t *testing.T) {
	err := pgErr("23503")
	if !IsForeignKeyViolation(err) {
		t.Fatal("expected IsForeignKeyViolation true")
	}
	if IsUniqueViolation(err) || IsCheckViolation(err) || IsNotNullViolation(err) || IsRetryable(err) {
		t.Fatal("expected all others false for 23503")
	}
}

func TestIsCheckViolation(t *testing.T) {
	err := pgErr("23514")
	if !IsCheckViolation(err) {
		t.Fatal("expected IsCheckViolation true")
	}
	if IsUniqueViolation(err) || IsForeignKeyViolation(err) || IsNotNullViolation(err) || IsRetryable(err) {
		t.Fatal("expected all others false for 23514")
	}
}

func TestIsNotNullViolation(t *testing.T) {
	err := pgErr("23502")
	if !IsNotNullViolation(err) {
		t.Fatal("expected IsNotNullViolation true")
	}
	if IsUniqueViolation(err) || IsForeignKeyViolation(err) || IsCheckViolation(err) || IsRetryable(err) {
		t.Fatal("expected all others false for 23502")
	}
}

func TestIsRetryableSerializationFail(t *testing.T) {
	err := pgErr("40001")
	if !IsRetryable(err) {
		t.Fatal("expected IsRetryable true for 40001")
	}
	if IsUniqueViolation(err) || IsForeignKeyViolation(err) || IsCheckViolation(err) || IsNotNullViolation(err) {
		t.Fatal("expected constraint checks false for 40001")
	}
}

func TestIsRetryableDeadlock(t *testing.T) {
	err := pgErr("40P01")
	if !IsRetryable(err) {
		t.Fatal("expected IsRetryable true for 40P01")
	}
	if IsUniqueViolation(err) || IsForeignKeyViolation(err) || IsCheckViolation(err) || IsNotNullViolation(err) {
		t.Fatal("expected constraint checks false for 40P01")
	}
}

func TestIsNoRows(t *testing.T) {
	if !IsNoRows(pgx.ErrNoRows) {
		t.Fatal("expected IsNoRows true for pgx.ErrNoRows")
	}
	if IsNoRows(nil) {
		t.Fatal("expected IsNoRows false for nil")
	}
}

func TestIsRetryableNil(t *testing.T) {
	if IsRetryable(nil) {
		t.Fatal("expected IsRetryable false for nil")
	}
}
