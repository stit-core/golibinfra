package pgkit

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrEmptyUUID — пустая строка на входе ParseUUID.
var ErrEmptyUUID = errors.New("pgkit: empty uuid")

// ErrInvalidUUID — строка на входе ParseUUID не разбирается как UUID.
var ErrInvalidUUID = errors.New("pgkit: invalid uuid")

// ParseUUID разбирает строку в pgtype.UUID; ошибки — ErrEmptyUUID и ErrInvalidUUID.
func ParseUUID(s string) (pgtype.UUID, error) {
	if s == "" {
		return pgtype.UUID{}, ErrEmptyUUID
	}
	parsed, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: %w", ErrInvalidUUID, err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

// UUIDString — каноническая строка UUID; невалидный (NULL) даёт "".
func UUIDString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

// NullableTime — NULL становится nil, иначе указатель на время.
func NullableTime(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// Timestamptz — обратное к NullableTime: nil → невалидный (NULL), иначе valid.
func Timestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
